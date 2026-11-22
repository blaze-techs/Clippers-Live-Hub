// Package caption generates burned-in subtitles with a local whisper.cpp build.
package caption

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/blaze-techs/Clippers-Live-Hub/internal/ffmpeg"
)

// Transcriber wraps a whisper.cpp CLI binary.
type Transcriber struct {
	Bin   string
	Model string
}

// Available reports whether captions can be generated.
func (t Transcriber) Available() bool {
	if t.Bin == "" || t.Model == "" {
		return false
	}
	if _, err := exec.LookPath(t.Bin); err != nil {
		return false
	}
	if _, err := os.Stat(t.Model); err != nil {
		return false
	}
	return true
}

// Transcribe extracts audio for [start, start+dur] and returns an SRT path.
// Timestamps in the SRT are relative to the start of the clip.
func (t Transcriber) Transcribe(ctx context.Context, tools *ffmpeg.Tools, input string, start, dur float64, workDir string) (string, error) {
	wav := filepath.Join(workDir, "speech.wav")
	prefix := filepath.Join(workDir, "speech")

	extract := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-ss", strconv.FormatFloat(start, 'f', 3, 64),
		"-t", strconv.FormatFloat(dur, 'f', 3, 64),
		"-i", input,
		"-vn", "-ac", "1", "-ar", "16000", wav,
	}
	if err := tools.RunQuiet(ctx, extract); err != nil {
		return "", fmt.Errorf("extract audio: %w", err)
	}

	cmd := exec.CommandContext(ctx, t.Bin,
		"-m", t.Model,
		"-f", wav,
		"-osrt",
		"-of", prefix,
		"-l", "auto",
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("whisper: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	srt := prefix + ".srt"
	if _, err := os.Stat(srt); err != nil {
		return "", fmt.Errorf("whisper produced no subtitles")
	}
	return srt, nil
}
