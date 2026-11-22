package analyze

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/blaze-techs/Clippers-Live-Hub/internal/ffmpeg"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/source"
)

var (
	ptsRe   = regexp.MustCompile(`pts_time:(-?[0-9.]+)`)
	rmsRe   = regexp.MustCompile(`lavfi\.astats\.Overall\.RMS_level=(-?[0-9.]+|-?inf)`)
	sceneRe = regexp.MustCompile(`lavfi\.scene_score=([0-9.]+)`)
)

// Parser turns ffmpeg metadata log lines into Samples.
//
// Audio and scene filters run in the same process, so their pts_time values
// share one clock; a scene score is attached to the next audio sample.
type Parser struct {
	hasAudio bool

	pts          float64
	lastRMS      float64
	pendingScene float64
}

// NewParser builds a parser for a stream with or without audio.
func NewParser(hasAudio bool) *Parser {
	return &Parser{hasAudio: hasAudio, lastRMS: -90}
}

// Line consumes one stderr line and reports a sample when one is complete.
func (p *Parser) Line(line string) (Sample, bool) {
	if m := ptsRe.FindStringSubmatch(line); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			p.pts = v
		}
	}
	if m := rmsRe.FindStringSubmatch(line); m != nil {
		v := -90.0
		if !strings.Contains(m[1], "inf") {
			if parsed, err := strconv.ParseFloat(m[1], 64); err == nil {
				v = parsed
			}
		}
		p.lastRMS = v
		s := Sample{T: p.pts, RMS: v, Scene: p.pendingScene}
		p.pendingScene = 0
		return s, true
	}
	if m := sceneRe.FindStringSubmatch(line); m != nil {
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return Sample{}, false
		}
		if p.hasAudio {
			if v > p.pendingScene {
				p.pendingScene = v
			}
			return Sample{}, false
		}
		return Sample{T: p.pts, RMS: -90, Scene: v}, true
	}
	return Sample{}, false
}

// Analyzer runs a standalone ffmpeg pass over a file.
type Analyzer struct {
	tools *ffmpeg.Tools
}

// New builds an analyzer.
func New(tools *ffmpeg.Tools) *Analyzer { return &Analyzer{tools: tools} }

// Run analyses a source until it ends or ctx is cancelled.
func (a *Analyzer) Run(ctx context.Context, src source.Source, hasAudio bool, onSample func(Sample)) error {
	filters := []string{}
	maps := []string{}
	if hasAudio {
		filters = append(filters,
			"[0:a]astats=metadata=1:reset=1,ametadata=print:key=lavfi.astats.Overall.RMS_level[a]")
		maps = append(maps, "[a]")
	}
	filters = append(filters,
		"[0:v]select='gte(scene,0)',metadata=print:key=lavfi.scene_score[v]")
	maps = append(maps, "[v]")

	args := []string{"-hide_banner", "-nostdin", "-loglevel", "level+info"}
	args = append(args, src.InputArgs()...)
	args = append(args, "-filter_complex", strings.Join(filters, ";"))
	for _, m := range maps {
		args = append(args, "-map", m)
	}
	args = append(args, "-f", "null", "-")

	parser := NewParser(hasAudio)
	err := a.tools.Run(ctx, args, func(line string) {
		if s, ok := parser.Line(line); ok {
			onSample(s)
		}
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("analyzer: %w", err)
	}
	return nil
}

// AnalyzeFile runs the analyzer over a bounded file and returns all samples.
func (a *Analyzer) AnalyzeFile(ctx context.Context, path string, hasAudio bool) ([]Sample, error) {
	var out []Sample
	src := source.Source{Kind: source.File, Path: path}
	err := a.Run(ctx, src, hasAudio, func(s Sample) { out = append(out, s) })
	return out, err
}
