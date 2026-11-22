// Package render turns a source range into a vertical, captioned clip.
package render

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/blaze-techs/Clippers-Live-Hub/internal/caption"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/face"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/ffmpeg"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/reframe"
)

// Options describes one export.
type Options struct {
	Tools   *ffmpeg.Tools
	Input   string
	Start   float64
	End     float64
	Out     string
	OutW    int
	OutH    int
	Layout  string
	Clean   bool
	Caption bool

	Transcriber caption.Transcriber
	WorkDir     string
	OnLog       func(string)
}

// Result summarises a finished clip.
type Result struct {
	Path      string
	Duration  float64
	Width     int
	Height    int
	Captioned bool
}

const trackFPS = 4.0
const trackWidth = 320

// Render produces a single vertical clip.
func Render(ctx context.Context, o Options) (*Result, error) {
	if o.Tools == nil {
		return nil, fmt.Errorf("ffmpeg tools required")
	}
	if o.OutW <= 0 || o.OutH <= 0 {
		o.OutW, o.OutH = 1080, 1920
	}
	if o.End <= o.Start {
		return nil, fmt.Errorf("empty range %.2f-%.2f", o.Start, o.End)
	}
	if o.WorkDir == "" {
		o.WorkDir = filepath.Dir(o.Out)
	}
	if err := os.MkdirAll(o.WorkDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(o.Out), 0o755); err != nil {
		return nil, err
	}

	info, err := o.Tools.Probe(ctx, o.Input)
	if err != nil {
		return nil, err
	}
	if info.Width == 0 || info.Height == 0 {
		return nil, fmt.Errorf("source has no video stream")
	}

	dur := o.End - o.Start
	geom := reframe.Compute(info.Width, info.Height, o.OutW, o.OutH)

	cmdPath := ""
	if o.Layout != "center" {
		path, err := trackSubject(ctx, o, geom, dur)
		if err != nil {
			o.logf("face tracking unavailable, falling back to centre crop: %v", err)
		} else {
			cmdPath = filepath.Join(o.WorkDir, "crop.cmd")
			if err := os.WriteFile(cmdPath, []byte(reframe.SendCmd(path, geom)), 0o644); err != nil {
				return nil, err
			}
		}
	}

	var srtPath string
	if o.Caption && o.Transcriber.Available() {
		srt, err := o.Transcriber.Transcribe(ctx, o.Tools, o.Input, o.Start, dur, o.WorkDir)
		if err != nil {
			o.logf("captions skipped: %v", err)
		} else {
			srtPath = srt
		}
	}

	chain := reframe.FilterChain(o.Layout, geom, cmdPath, o.OutW, o.OutH)
	if srtPath != "" {
		chain += ",subtitles=" + escapeFilterPath(srtPath) +
			":force_style='FontName=DejaVu Sans,FontSize=16,Alignment=2,MarginV=90,BorderStyle=1,Outline=2,Shadow=0'"
	}

	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-ss", strconv.FormatFloat(o.Start, 'f', 3, 64),
		"-t", strconv.FormatFloat(dur, 'f', 3, 64),
		"-i", o.Input,
		"-vf", chain,
	}
	if info.HasAudio {
		if o.Clean {
			args = append(args, "-af", "highpass=f=70,afftdn=nf=-25,loudnorm=I=-14:TP=-1.5:LRA=11")
		}
		args = append(args, "-c:a", "aac", "-b:a", "192k", "-ar", "48000", "-ac", "2")
	} else {
		args = append(args, "-an")
	}
	args = append(args,
		"-c:v", "libx264",
		"-preset", "medium",
		"-crf", "20",
		"-pix_fmt", "yuv420p",
		"-profile:v", "high",
		"-level", "4.1",
		"-movflags", "+faststart",
		o.Out,
	)

	if err := o.Tools.RunQuiet(ctx, args); err != nil {
		return nil, err
	}
	return &Result{
		Path:      o.Out,
		Duration:  dur,
		Width:     o.OutW,
		Height:    o.OutH,
		Captioned: srtPath != "",
	}, nil
}

// trackSubject samples the source range and returns a smoothed crop path.
func trackSubject(ctx context.Context, o Options, geom reframe.Geometry, dur float64) ([]reframe.Point, error) {
	frameH := int(math.Round(float64(geom.SrcH) * trackWidth / float64(geom.SrcW)))
	if frameH%2 != 0 {
		frameH--
	}
	if frameH <= 0 {
		return nil, fmt.Errorf("invalid frame height")
	}

	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-ss", strconv.FormatFloat(o.Start, 'f', 3, 64),
		"-t", strconv.FormatFloat(dur, 'f', 3, 64),
		"-i", o.Input,
		"-vf", fmt.Sprintf("fps=%g,scale=%d:%d", trackFPS, trackWidth, frameH),
		"-f", "rawvideo", "-pix_fmt", "gray", "-",
	}
	cmd := exec.CommandContext(ctx, o.Tools.FFmpeg, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	detector := face.NewDetector()
	frameSize := trackWidth * frameH
	buf := make([]byte, frameSize)
	reader := bufio.NewReaderSize(stdout, frameSize)

	var track []reframe.Point
	var lastX, lastY float64 = 0.5, 0.5
	for {
		if _, err := io.ReadFull(reader, buf); err != nil {
			break
		}
		t := float64(len(track)) / trackFPS
		if rect := face.Largest(detector.DetectGray(buf, trackWidth, frameH)); rect != nil {
			lastX = rect.CenterX() / trackWidth
			lastY = rect.CenterY() / float64(frameH)
		}
		track = append(track, reframe.Point{T: t, X: lastX, Y: lastY})
	}
	if err := cmd.Wait(); err != nil && len(track) == 0 {
		return nil, fmt.Errorf("frame sampling: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if len(track) == 0 {
		return nil, fmt.Errorf("no frames sampled")
	}
	return reframe.BuildPath(track, geom, 0.18, 0.03), nil
}

// Thumbnail writes a JPEG preview of the clip.
func Thumbnail(ctx context.Context, tools *ffmpeg.Tools, clip, dest string, at float64) error {
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-ss", strconv.FormatFloat(at, 'f', 2, 64),
		"-i", clip,
		"-frames:v", "1",
		"-vf", "scale=360:-2",
		"-q:v", "3",
		dest,
	}
	return tools.RunQuiet(ctx, args)
}

func escapeFilterPath(p string) string {
	p = filepath.ToSlash(p)
	p = strings.ReplaceAll(p, `\`, `/`)
	p = strings.ReplaceAll(p, `:`, `\:`)
	p = strings.ReplaceAll(p, `'`, `\'`)
	return "'" + p + "'"
}

func (o Options) logf(format string, args ...any) {
	if o.OnLog != nil {
		o.OnLog(fmt.Sprintf(format, args...))
	}
}
