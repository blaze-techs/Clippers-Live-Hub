// Package media imports sources: probe metadata, extract preview frames and
// build waveform peaks for the timeline.
package media

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/blaze-techs/Clippers-Live-Hub/internal/ffmpeg"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/project"
)

// Importer turns a file on disk into a project media entry.
type Importer struct {
	Tools *ffmpeg.Tools
}

// Import probes the file, writes preview frames into thumbsDir and records
// waveform peaks. A failed thumbnail or waveform is not fatal: the media stays
// usable, it just renders without those hints.
func (im *Importer) Import(ctx context.Context, p *project.Project, path, thumbsDir string) (*project.Media, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", path, err)
	}
	info, err := im.Tools.Probe(ctx, path)
	if err != nil {
		return nil, err
	}
	if !info.HasVideo && !info.HasAudio {
		return nil, fmt.Errorf("%s has no audio or video streams", filepath.Base(path))
	}

	m := &project.Media{
		ID:       project.NewID(),
		Name:     filepath.Base(path),
		Path:     path,
		Duration: info.Duration,
		Width:    info.Width,
		Height:   info.Height,
		FPS:      info.FPS,
		HasAudio: info.HasAudio,
		HasVideo: info.HasVideo,
		AddedAt:  time.Now().UTC(),
	}
	if m.Duration <= 0 {
		return nil, fmt.Errorf("%s has no readable duration", filepath.Base(path))
	}

	if m.HasVideo {
		for i, frac := range []float64{0.08, 0.5, 0.92} {
			name := fmt.Sprintf("%s_%d.jpg", m.ID, i)
			dest := filepath.Join(thumbsDir, name)
			if err := im.thumbnail(ctx, path, dest, m.Duration*frac); err == nil {
				m.Thumbs = append(m.Thumbs, name)
			}
		}
	}
	if m.HasAudio {
		if peaks, err := im.peaks(ctx, path, m.Duration); err == nil {
			m.Peaks = peaks
		}
	}
	return m, nil
}

func (im *Importer) thumbnail(ctx context.Context, src, dest string, at float64) error {
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-ss", strconv.FormatFloat(at, 'f', 3, 64),
		"-i", src, "-frames:v", "1",
		"-vf", "scale=320:-2", "-q:v", "4", dest,
	}
	return im.Tools.RunQuiet(ctx, args)
}

// peaks decodes mono 8 kHz PCM and reduces it to normalised bucket maxima.
func (im *Importer) peaks(ctx context.Context, src string, duration float64) ([]float64, error) {
	const rate = 8000
	const buckets = 300
	limit := duration
	if limit > 900 {
		limit = 900
	}
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-i", src, "-vn", "-ac", "1", "-ar", strconv.Itoa(rate),
		"-t", strconv.FormatFloat(limit, 'f', 2, 64),
		"-f", "s16le", "-",
	}
	cmd := exec.CommandContext(ctx, im.Tools.FFmpeg, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("decode audio: %w %s", err, errb.String())
	}
	raw := out.Bytes()
	samples := len(raw) / 2
	if samples == 0 {
		return nil, fmt.Errorf("no audio samples")
	}

	peaks := make([]float64, buckets)
	per := samples / buckets
	if per == 0 {
		per = 1
	}
	var maxPeak float64
	for b := 0; b < buckets; b++ {
		start := b * per
		end := start + per
		if end > samples {
			end = samples
		}
		var peak float64
		for i := start; i < end; i++ {
			v := int16(uint16(raw[i*2]) | uint16(raw[i*2+1])<<8)
			a := math.Abs(float64(v))
			if a > peak {
				peak = a
			}
		}
		peaks[b] = peak
		if peak > maxPeak {
			maxPeak = peak
		}
	}
	if maxPeak > 0 {
		for i := range peaks {
			peaks[i] = math.Round(peaks[i]/maxPeak*100) / 100
		}
	}
	return peaks, nil
}
