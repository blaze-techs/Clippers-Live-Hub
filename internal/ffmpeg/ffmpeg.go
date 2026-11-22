// Package ffmpeg wraps the ffmpeg/ffprobe binaries.
package ffmpeg

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// Tools holds resolved binary paths.
type Tools struct {
	FFmpeg  string
	FFprobe string
}

// Locate resolves ffmpeg and ffprobe, preferring explicit paths, then a
// bin/ directory next to the executable, then PATH.
func Locate(ffmpegPath, ffprobePath string) (*Tools, error) {
	t := &Tools{
		FFmpeg:  resolve(ffmpegPath, "ffmpeg"),
		FFprobe: resolve(ffprobePath, "ffprobe"),
	}
	if t.FFmpeg == "" {
		return nil, fmt.Errorf("ffmpeg not found: install it or set ffmpeg_path")
	}
	if t.FFprobe == "" {
		// ffprobe is optional; fall back to ffmpeg's sibling path.
		if p, err := exec.LookPath(t.FFmpeg); err == nil {
			candidate := filepath.Join(filepath.Dir(p), exeName("ffprobe"))
			if _, err := os.Stat(candidate); err == nil {
				t.FFprobe = candidate
			}
		}
	}
	return t, nil
}

func exeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func resolve(explicit, name string) string {
	if explicit != "" {
		if p, err := exec.LookPath(explicit); err == nil {
			return p
		}
		if _, err := os.Stat(explicit); err == nil {
			return explicit
		}
	}
	if self, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), "bin", exeName(name))
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// MediaInfo is a trimmed view of ffprobe output.
type MediaInfo struct {
	Duration float64
	Width    int
	Height   int
	FPS      float64
	HasAudio bool
	HasVideo bool
}

// Probe inspects a media file or stream.
func (t *Tools) Probe(ctx context.Context, path string) (*MediaInfo, error) {
	if t.FFprobe == "" {
		return nil, fmt.Errorf("ffprobe unavailable")
	}
	cmd := exec.CommandContext(ctx, t.FFprobe,
		"-v", "error",
		"-show_entries", "stream=codec_type,width,height,r_frame_rate:format=duration",
		"-of", "json", path)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe %s: %w", path, err)
	}
	var parsed struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			RFrame    string `json:"r_frame_rate"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, err
	}
	info := &MediaInfo{}
	for _, s := range parsed.Streams {
		switch s.CodecType {
		case "video":
			if !info.HasVideo {
				info.HasVideo = true
				info.Width, info.Height = s.Width, s.Height
				info.FPS = parseRate(s.RFrame)
			}
		case "audio":
			info.HasAudio = true
		}
	}
	if d, err := strconv.ParseFloat(parsed.Format.Duration, 64); err == nil {
		info.Duration = d
	}
	return info, nil
}

func parseRate(s string) float64 {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		v, _ := strconv.ParseFloat(s, 64)
		return v
	}
	num, _ := strconv.ParseFloat(parts[0], 64)
	den, _ := strconv.ParseFloat(parts[1], 64)
	if den == 0 {
		return 0
	}
	return num / den
}

// Run executes ffmpeg and streams stderr lines to onLine.
func (t *Tools) Run(ctx context.Context, args []string, onLine func(string)) error {
	cmd := exec.CommandContext(ctx, t.FFmpeg, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if onLine != nil {
				onLine(sc.Text())
			}
		}
	}()
	wg.Wait()
	return cmd.Wait()
}

// RunQuiet executes ffmpeg, capturing stderr for error reporting.
func (t *Tools) RunQuiet(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, t.FFmpeg, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		if len(msg) > 1200 {
			msg = msg[len(msg)-1200:]
		}
		return fmt.Errorf("ffmpeg failed: %w: %s", err, strings.TrimSpace(msg))
	}
	return nil
}

// Concat joins MPEG-TS segments losslessly into dest.
func (t *Tools) Concat(ctx context.Context, segments []string, dest string) error {
	listPath := dest + ".concat.txt"
	var b strings.Builder
	for _, s := range segments {
		b.WriteString("file '")
		b.WriteString(strings.ReplaceAll(s, "'", `'\''`))
		b.WriteString("'\n")
	}
	if err := os.WriteFile(listPath, []byte(b.String()), 0o644); err != nil {
		return err
	}
	defer os.Remove(listPath)
	return t.RunQuiet(ctx, []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-f", "concat", "-safe", "0", "-i", listPath,
		"-c", "copy", dest,
	})
}
