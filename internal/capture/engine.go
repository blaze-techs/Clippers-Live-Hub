package capture

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/blaze-techs/Clippers-Live-Hub/internal/analyze"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/ffmpeg"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/source"
)

// Engine records a live source into a rolling buffer while emitting analysis
// samples from the same ffmpeg process, so segment times and sample times
// share one clock.
type Engine struct {
	tools *ffmpeg.Tools
	ring  *Ring
}

// NewEngine builds an engine writing segments into ring.
func NewEngine(tools *ffmpeg.Tools, ring *Ring) *Engine {
	return &Engine{tools: tools, ring: ring}
}

// Ring exposes the underlying buffer.
func (e *Engine) Ring() *Ring { return e.ring }

// Run captures until ctx is cancelled or the source ends.
func (e *Engine) Run(ctx context.Context, src source.Source, hasAudio bool, onSample func(analyze.Sample)) error {
	listPath := filepath.Join(e.ring.dir, "segments.csv")
	pattern := filepath.Join(e.ring.dir, "seg_%06d.ts")

	var filters []string
	if hasAudio {
		filters = append(filters,
			"[0:v]split=2[venc][vdet];"+
				"[vdet]select='gte(scene,0)',metadata=print:key=lavfi.scene_score[vout];"+
				"[0:a]asplit=2[aenc][adet];"+
				"[adet]astats=metadata=1:reset=1,ametadata=print:key=lavfi.astats.Overall.RMS_level[aout]")
	} else {
		filters = append(filters,
			"[0:v]split=2[venc][vdet];"+
				"[vdet]select='gte(scene,0)',metadata=print:key=lavfi.scene_score[vout]")
	}

	args := []string{"-hide_banner", "-nostdin", "-loglevel", "level+info"}
	args = append(args, src.InputArgs()...)
	args = append(args, "-filter_complex", strings.Join(filters, ";"))

	// Output 1: rolling segments.
	args = append(args, "-map", "[venc]")
	if hasAudio {
		args = append(args, "-map", "[aenc]")
	}
	args = append(args,
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-tune", "zerolatency",
		"-pix_fmt", "yuv420p",
		"-g", "60",
		"-sc_threshold", "0",
	)
	if hasAudio {
		args = append(args, "-c:a", "aac", "-b:a", "128k", "-ar", "48000", "-ac", "2")
	}
	args = append(args,
		"-f", "segment",
		"-segment_time", strconv.FormatFloat(e.ring.segTime, 'f', 2, 64),
		"-segment_format", "mpegts",
		"-reset_timestamps", "1",
		"-segment_list", listPath,
		"-segment_list_type", "csv",
		"-segment_list_flags", "+live",
		"-segment_list_size", "0",
		pattern,
	)

	// Output 2: analysis sink.
	args = append(args, "-map", "[vout]")
	if hasAudio {
		args = append(args, "-map", "[aout]")
	}
	args = append(args, "-f", "null", "-")

	stop := make(chan struct{})
	go e.tailList(ctx, listPath, stop)
	defer close(stop)

	parser := analyze.NewParser(hasAudio)
	err := e.tools.Run(ctx, args, func(line string) {
		if s, ok := parser.Line(line); ok {
			onSample(s)
		}
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("capture engine: %w", err)
	}
	return nil
}
