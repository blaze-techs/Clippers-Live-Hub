// Package compose turns a project timeline into a single ffmpeg render.
//
// One filter graph does everything: source trims, crop/scale to the canvas,
// picture-in-picture placement, time shifting onto the timeline, overlay
// compositing, audio mixing, loudness normalisation and burned-in captions.
// Rendering from the same document the preview reads is what keeps the preview
// and the export in agreement.
package compose

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/blaze-techs/Clippers-Live-Hub/internal/ffmpeg"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/project"
)

// Options configures one render.
type Options struct {
	Tools    *ffmpeg.Tools
	Project  *project.Project
	Out      string
	WorkDir  string
	OnLog    func(string)
	Progress func(frac float64)
}

// Result describes a finished render.
type Result struct {
	Path     string  `json:"path"`
	Duration float64 `json:"duration"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Size     int64   `json:"size"`
}

// ErrEmpty means the timeline has nothing to render.
var ErrEmpty = fmt.Errorf("timeline is empty: add a clip first")

// Run renders the project.
func Run(ctx context.Context, o Options) (*Result, error) {
	if o.Tools == nil {
		return nil, fmt.Errorf("ffmpeg tools required")
	}
	p := o.Project
	if p == nil {
		return nil, fmt.Errorf("project required")
	}
	p.Validate()
	dur := p.Duration()
	if dur <= 0 {
		return nil, ErrEmpty
	}
	if o.WorkDir == "" {
		o.WorkDir = os.TempDir()
	}
	if err := os.MkdirAll(o.WorkDir, 0o755); err != nil {
		return nil, err
	}

	graph, inputs, err := buildGraph(p, dur, o.WorkDir)
	if err != nil {
		return nil, err
	}

	exp := p.Export
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	args = append(args, inputs...)
	args = append(args,
		"-filter_complex", graph,
		"-map", "[vout]", "-map", "[aout]",
		"-c:v", "libx264", "-preset", exp.Preset, "-crf", strconv.Itoa(exp.CRF),
		"-pix_fmt", "yuv420p", "-r", trimFloat(exp.FPS),
		"-c:a", "aac", "-b:a", exp.ABitrate, "-ar", "48000", "-ac", "2",
		"-movflags", "+faststart",
		"-t", trimFloat(dur),
		"-progress", "pipe:1", "-nostats",
		o.Out,
	)

	cmd := exec.CommandContext(ctx, o.Tools.FFmpeg, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if o.OnLog != nil {
		o.OnLog("ffmpeg " + strings.Join(args, " "))
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "out_time_ms=") {
			continue
		}
		us, err := strconv.ParseFloat(strings.TrimPrefix(line, "out_time_ms="), 64)
		if err != nil || o.Progress == nil {
			continue
		}
		frac := (us / 1e6) / dur
		if frac < 0 {
			frac = 0
		}
		if frac > 1 {
			frac = 1
		}
		o.Progress(frac)
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("ffmpeg failed: %w: %s", err, tail(stderr.String(), 1200))
	}

	info, err := o.Tools.Probe(ctx, o.Out)
	if err != nil {
		return nil, fmt.Errorf("verify output: %w", err)
	}
	if info.Duration <= 0 || !info.HasVideo {
		return nil, fmt.Errorf("output failed verification: no video stream")
	}
	st, err := os.Stat(o.Out)
	if err != nil {
		return nil, err
	}
	if o.Progress != nil {
		o.Progress(1)
	}
	return &Result{
		Path: o.Out, Duration: info.Duration, Width: info.Width,
		Height: info.Height, Size: st.Size(),
	}, nil
}

type mediaInput struct {
	index int
	info  *project.Media
}

// buildGraph returns the filter_complex string and the -i arguments.
func buildGraph(p *project.Project, dur float64, workDir string) (string, []string, error) {
	var inputs []string
	index := map[string]mediaInput{}
	next := 0
	for _, t := range p.Tracks {
		for _, c := range t.Clips {
			if _, ok := index[c.MediaID]; ok {
				continue
			}
			m := p.MediaByID(c.MediaID)
			if m == nil {
				return "", nil, fmt.Errorf("clip %q references missing media %q", c.ID, c.MediaID)
			}
			path := p.Resolve(*m)
			if _, err := os.Stat(path); err != nil {
				return "", nil, fmt.Errorf("media %q is missing: %s", m.Name, path)
			}
			inputs = append(inputs, "-i", path)
			index[c.MediaID] = mediaInput{index: next, info: m}
			next++
		}
	}
	if len(inputs) == 0 {
		return "", nil, ErrEmpty
	}

	W, H := p.Width, p.Height
	var f []string
	f = append(f, fmt.Sprintf("color=c=0x0B0B10:s=%dx%d:r=%s:d=%s[base]",
		W, H, trimFloat(p.FPS), trimFloat(dur)))

	label := "base"
	vidN, audN := 0, 0
	var audioLabels []string

	for _, t := range p.Tracks {
		if t.Hidden {
			continue
		}
		for _, c := range t.Clips {
			in := index[c.MediaID]
			if c.End() <= 0 {
				continue
			}
			if t.Kind != project.TrackAudio && in.info.HasVideo {
				f = append(f, fmt.Sprintf("[%d:v]%s[v%d]", in.index, videoChain(c, W, H), vidN))
				if c.PiP != nil {
					x := int(c.PiP.X * float64(W))
					y := int(c.PiP.Y * float64(H))
					f = append(f, fmt.Sprintf("[%s][v%d]overlay=%d:%d:eof_action=pass:enable='between(t,%s,%s)'[o%d]",
						label, vidN, x, y, trimFloat(c.Start), trimFloat(c.End()), vidN))
				} else {
					f = append(f, fmt.Sprintf("[%s][v%d]overlay=0:0:eof_action=pass:enable='between(t,%s,%s)'[o%d]",
						label, vidN, trimFloat(c.Start), trimFloat(c.End()), vidN))
				}
				label = fmt.Sprintf("o%d", vidN)
				vidN++
			}
			if in.info.HasAudio && !t.Muted && c.Volume > 0 {
				lbl, chain := audioChain(in.index, c, audN)
				f = append(f, chain...)
				audioLabels = append(audioLabels, lbl)
				audN++
			}
		}
	}

	// Captions and watermark are applied to the composited picture.
	if p.Export.Burn && len(p.Cues) > 0 {
		assPath := filepath.Join(workDir, "captions.ass")
		if err := os.WriteFile(assPath, []byte(project.BuildASS(p)), 0o644); err != nil {
			return "", nil, err
		}
		f = append(f, fmt.Sprintf("[%s]subtitles='%s'[vcap]", label, escapePath(assPath)))
		label = "vcap"
	}
	if wm := strings.TrimSpace(p.Export.Watermark); wm != "" {
		f = append(f, fmt.Sprintf("[%s]drawtext=text='%s':x=w-tw-40:y=40:fontsize=36:fontcolor=white@0.85:box=1:boxcolor=black@0.35:boxborderw=12[vwm]",
			label, escapeText(wm)))
		label = "vwm"
	}
	f = append(f, fmt.Sprintf("[%s]format=yuv420p[vout]", label))

	if len(audioLabels) > 0 {
		joined := strings.Join(audioLabels, "")
		if len(audioLabels) == 1 {
			f = append(f, fmt.Sprintf("%samix=inputs=1:normalize=0:dropout_transition=0[amixed]", joined))
		} else {
			f = append(f, fmt.Sprintf("%samix=inputs=%d:normalize=0:dropout_transition=0[amixed]",
				joined, len(audioLabels)))
		}
		if p.Export.Clean {
			f = append(f, "[amixed]highpass=f=80,afftdn=nr=12:nf=-30,loudnorm=I=-14:TP=-1.5:LRA=11[aout]")
		} else {
			f = append(f, "[amixed]anull[aout]")
		}
	} else {
		f = append(f, fmt.Sprintf("anullsrc=r=48000:cl=stereo,atrim=0:%s[aout]", trimFloat(dur)))
	}

	return strings.Join(f, ";"), inputs, nil
}

// videoChain builds the per-clip filter producing a canvas-sized frame, then
// shifts it to its timeline position.
func videoChain(c project.Clip, W, H int) string {
	targetW, targetH := W, H
	if c.PiP != nil {
		targetW = int(c.PiP.W * float64(W))
		targetH = int(c.PiP.H * float64(H))
	}
	targetW, targetH = even(targetW), even(targetH)

	steps := []string{
		fmt.Sprintf("trim=start=%s:end=%s", trimFloat(c.In), trimFloat(c.Out)),
		"setpts=PTS-STARTPTS",
	}
	if crop, ok := cropFilter(c); ok {
		steps = append(steps, crop)
	}
	steps = append(steps,
		fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase", targetW, targetH),
		fmt.Sprintf("crop=%d:%d", targetW, targetH),
		"setsar=1",
		fmt.Sprintf("setpts=PTS+%s/TB", trimFloat(c.Start)),
	)
	return strings.Join(steps, ",")
}

// cropFilter emits the source crop. Keyframed clips get a piecewise-linear
// pan so the framing follows the subject in the exported file, not just in the
// preview. Zoom changes are not interpolated: crop needs a constant output
// size, so width and height come from the first keyframe.
func cropFilter(c project.Clip) (string, bool) {
	if len(c.Keyframes) == 0 {
		if c.Crop == nil {
			return "", false
		}
		r := *c.Crop
		return fmt.Sprintf("crop=iw*%s:ih*%s:iw*%s:ih*%s",
			trimFloat(r.W), trimFloat(r.H), trimFloat(r.X), trimFloat(r.Y)), true
	}
	first := c.Keyframes[0].Rect
	x := piecewise(c.Keyframes, func(r project.Rect) float64 { return r.X })
	y := piecewise(c.Keyframes, func(r project.Rect) float64 { return r.Y })
	return fmt.Sprintf("crop=iw*%s:ih*%s:iw*(%s):ih*(%s)",
		trimFloat(first.W), trimFloat(first.H), x, y), true
}

// piecewise interpolates a rectangle component across keyframes. Commas are
// escaped because they separate filters inside filter_complex.
func piecewise(kfs []project.Keyframe, get func(project.Rect) float64) string {
	n := len(kfs)
	expr := trimFloat(get(kfs[n-1].Rect))
	for i := n - 2; i >= 0; i-- {
		t0, t1 := kfs[i].T, kfs[i+1].T
		v0, v1 := get(kfs[i].Rect), get(kfs[i+1].Rect)
		dt := t1 - t0
		if dt <= 0 {
			dt = 0.001
		}
		seg := fmt.Sprintf("%s+(%s-%s)*(t-%s)/%s",
			trimFloat(v0), trimFloat(v1), trimFloat(v0), trimFloat(t0), trimFloat(dt))
		expr = fmt.Sprintf(`if(lt(t\,%s)\,%s\,%s)`, trimFloat(t1), seg, expr)
	}
	return expr
}

func audioChain(input int, c project.Clip, n int) (string, []string) {
	delayMs := int(c.Start * 1000)
	var steps []string
	steps = append(steps, fmt.Sprintf("atrim=start=%s:end=%s", trimFloat(c.In), trimFloat(c.Out)))
	steps = append(steps, "asetpts=PTS-STARTPTS")
	steps = append(steps, "aresample=48000")
	vol := c.Volume
	if vol <= 0 {
		vol = 1
	}
	steps = append(steps, "volume="+trimFloat(vol))
	if c.FadeIn > 0 {
		steps = append(steps, fmt.Sprintf("afade=t=in:st=0:d=%s", trimFloat(c.FadeIn)))
	}
	if c.FadeOut > 0 {
		start := c.Length() - c.FadeOut
		if start < 0 {
			start = 0
		}
		steps = append(steps, fmt.Sprintf("afade=t=out:st=%s:d=%s", trimFloat(start), trimFloat(c.FadeOut)))
	}
	if delayMs > 0 {
		steps = append(steps, fmt.Sprintf("adelay=%d|%d", delayMs, delayMs))
	}
	label := fmt.Sprintf("[a%d]", n)
	return label, []string{fmt.Sprintf("[%d:a]%s%s", input, strings.Join(steps, ","), label)}
}

func even(v int) int {
	if v < 2 {
		return 2
	}
	if v%2 != 0 {
		return v - 1
	}
	return v
}

func trimFloat(v float64) string {
	s := strconv.FormatFloat(v, 'f', 3, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// escapePath makes a path safe inside a filter argument.
func escapePath(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	p = strings.ReplaceAll(p, ":", `\:`)
	return p
}

func escapeText(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, ":", `\:`)
	s = strings.ReplaceAll(s, "'", `\'`)
	s = strings.ReplaceAll(s, "%", `\%`)
	return s
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
