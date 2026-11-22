// Package pipeline orchestrates capture, detection and export.
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blaze-techs/Clippers-Live-Hub/internal/analyze"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/caption"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/capture"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/config"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/ffmpeg"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/render"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/source"
)

// Clip is a finished export.
type Clip struct {
	ID        string    `json:"id"`
	File      string    `json:"file"`
	Thumb     string    `json:"thumb"`
	Start     float64   `json:"start"`
	End       float64   `json:"end"`
	Duration  float64   `json:"duration"`
	Size      int64     `json:"size"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	Captioned bool      `json:"captioned"`
	CreatedAt time.Time `json:"created_at"`
}

// Status is the live state shown in the UI.
type Status struct {
	Running     bool      `json:"running"`
	Mode        string    `json:"mode"`
	Source      string    `json:"source"`
	Score       float64   `json:"score"`
	Baseline    float64   `json:"baseline"`
	BufferStart float64   `json:"buffer_start"`
	BufferEnd   float64   `json:"buffer_end"`
	Highlights  int       `json:"highlights"`
	ClipCount   int       `json:"clip_count"`
	Message     string    `json:"message"`
	StartedAt   time.Time `json:"started_at"`
}

// Event is broadcast to SSE subscribers.
type Event struct {
	Type    string  `json:"type"`
	Message string  `json:"message,omitempty"`
	Score   float64 `json:"score,omitempty"`
	Clip    *Clip   `json:"clip,omitempty"`
	Status  *Status `json:"status,omitempty"`
}

// Pipeline owns the running session.
type Pipeline struct {
	cfg   config.Config
	tools *ffmpeg.Tools

	mu      sync.RWMutex
	status  Status
	clips   []Clip
	cancel  context.CancelFunc
	done    chan struct{}
	subs    map[int]chan Event
	nextSub int
	seq     int
}

// New builds a pipeline and loads any previously exported clips.
func New(cfg config.Config, tools *ffmpeg.Tools) *Pipeline {
	os.MkdirAll(cfg.OutputDir, 0o755)
	p := &Pipeline{cfg: cfg, tools: tools, subs: map[int]chan Event{}}
	p.loadIndex()
	p.status.ClipCount = len(p.clips)
	return p
}

// Start begins a session for the given source.
func (p *Pipeline) Start(src source.Source) error {
	if err := src.Validate(); err != nil {
		return err
	}
	p.mu.Lock()
	if p.status.Running {
		p.mu.Unlock()
		return errors.New("a session is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	mode := "live"
	if src.Kind == source.File {
		mode = "file"
	}
	p.status = Status{
		Running:   true,
		Mode:      mode,
		Source:    src.Label(),
		StartedAt: time.Now(),
		ClipCount: len(p.clips),
	}
	p.mu.Unlock()

	p.broadcast(Event{Type: "status", Status: p.statusPtr()})

	go func() {
		defer close(p.done)
		defer cancel()
		var err error
		if mode == "file" {
			err = p.runFile(ctx, src)
		} else {
			err = p.runLive(ctx, src)
		}
		p.mu.Lock()
		p.status.Running = false
		if err != nil && !errors.Is(err, context.Canceled) {
			p.status.Message = err.Error()
		} else if err == nil {
			p.status.Message = "finished"
		}
		p.mu.Unlock()
		p.broadcast(Event{Type: "status", Status: p.statusPtr()})
		p.broadcast(Event{Type: "done", Message: p.status.Message})
	}()
	return nil
}

// Stop cancels the running session and waits for cleanup.
func (p *Pipeline) Stop() {
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		select {
		case <-done:
		case <-time.After(15 * time.Second):
		}
	}
}

// Status returns a snapshot.
func (p *Pipeline) Status() Status {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s := p.status
	s.ClipCount = len(p.clips)
	return s
}

// Clips returns exports newest first.
func (p *Pipeline) Clips() []Clip {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Clip, len(p.clips))
	copy(out, p.clips)
	return out
}

// Subscribe registers an event listener.
func (p *Pipeline) Subscribe() (int, <-chan Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := p.nextSub
	p.nextSub++
	ch := make(chan Event, 64)
	p.subs[id] = ch
	return id, ch
}

// Unsubscribe removes a listener.
func (p *Pipeline) Unsubscribe(id int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ch, ok := p.subs[id]; ok {
		delete(p.subs, id)
		close(ch)
	}
}

func (p *Pipeline) broadcast(ev Event) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, ch := range p.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (p *Pipeline) logf(format string, args ...any) {
	p.broadcast(Event{Type: "log", Message: fmt.Sprintf(format, args...)})
}

// runLive records a rolling buffer and clips detected highlights.
func (p *Pipeline) runLive(ctx context.Context, src source.Source) error {
	bufDir := filepath.Join(p.cfg.OutputDir, ".buffer")
	ring, err := capture.NewRing(bufDir, 2, p.cfg.BufferSeconds)
	if err != nil {
		return err
	}
	defer ring.Reset()

	engine := capture.NewEngine(p.tools, ring)
	hasAudio := src.HasAudioInput()

	detector := analyze.NewDetector(analyze.Options{
		Threshold:    p.cfg.Threshold,
		PreRoll:      p.cfg.PreRoll,
		PostRoll:     p.cfg.PostRoll,
		MinClip:      p.cfg.MinClip,
		MaxClip:      p.cfg.MaxClip,
		Cooldown:     p.cfg.Cooldown,
		BaselineSecs: p.cfg.BaselineSecs,
		HasAudio:     hasAudio,
	})

	jobs := make(chan analyze.Highlight, 8)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.clipWorker(ctx, ring, jobs)
	}()

	// Refresh the buffer span in the UI.
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				start, end := ring.Span()
				p.mu.Lock()
				p.status.BufferStart, p.status.BufferEnd = start, end
				p.mu.Unlock()
			}
		}
	}()

	p.logf("capturing %s", src.Label())
	err = engine.Run(ctx, src, hasAudio, func(s analyze.Sample) {
		score := detector.Normalise(s)
		p.mu.Lock()
		p.status.Score = score
		p.status.Baseline = detector.Baseline()
		p.mu.Unlock()

		if h := detector.Add(s); h != nil {
			p.mu.Lock()
			p.status.Highlights++
			p.mu.Unlock()
			p.logf("highlight at %.1fs (score %.2f)", h.PeakTime, h.Score)
			select {
			case jobs <- *h:
			case <-ctx.Done():
			}
		}
	})
	close(jobs)
	wg.Wait()
	return err
}

func (p *Pipeline) clipWorker(ctx context.Context, ring *capture.Ring, jobs <-chan analyze.Highlight) {
	for {
		select {
		case <-ctx.Done():
			return
		case h, ok := <-jobs:
			if !ok {
				return
			}
			if err := p.renderFromRing(ctx, ring, h); err != nil {
				p.logf("clip failed: %v", err)
			}
		}
	}
}

func (p *Pipeline) renderFromRing(ctx context.Context, ring *capture.Ring, h analyze.Highlight) error {
	segs := ring.Cover(h.Start, h.End)
	if len(segs) == 0 {
		return fmt.Errorf("buffer does not cover %.1f-%.1f yet", h.Start, h.End)
	}
	base := segs[0].Start
	start := h.Start - base
	end := h.End - base
	last := segs[len(segs)-1]
	if h.End > last.End {
		end = last.End - base
	}
	if start < 0 {
		start = 0
	}
	if end-start < p.cfg.MinClip*0.5 {
		return fmt.Errorf("not enough buffered footage for %.1f-%.1f", h.Start, h.End)
	}

	work, err := os.MkdirTemp(p.cfg.OutputDir, "work-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	joined := filepath.Join(work, "joined.ts")
	paths := make([]string, len(segs))
	for i, s := range segs {
		paths[i] = s.Path
	}
	if err := p.tools.Concat(ctx, paths, joined); err != nil {
		return err
	}
	return p.export(ctx, joined, start, end, work, h)
}

func (p *Pipeline) export(ctx context.Context, input string, start, end float64, work string, h analyze.Highlight) error {
	clip := p.newClipMeta(h)
	outPath := filepath.Join(p.cfg.OutputDir, clip.File)

	res, err := render.Render(ctx, render.Options{
		Tools:   p.tools,
		Input:   input,
		Start:   start,
		End:     end,
		Out:     outPath,
		OutW:    p.cfg.OutWidth,
		OutH:    p.cfg.OutHeight,
		Layout:  p.cfg.Layout,
		Clean:   p.cfg.CleanAudio,
		Caption: p.cfg.Captions,
		Transcriber: caption.Transcriber{
			Bin:   p.cfg.WhisperBin,
			Model: p.cfg.WhisperModel,
		},
		WorkDir: work,
		OnLog:   func(msg string) { p.logf("%s", msg) },
	})
	if err != nil {
		return err
	}

	clip.Duration = res.Duration
	clip.Width, clip.Height = res.Width, res.Height
	clip.Captioned = res.Captioned
	thumbName := strings.TrimSuffix(clip.File, ".mp4") + ".jpg"
	thumbPath := filepath.Join(p.cfg.OutputDir, thumbName)
	if err := render.Thumbnail(ctx, p.tools, outPath, thumbPath, 0.5); err == nil {
		clip.Thumb = thumbName
	}
	if info, err := os.Stat(outPath); err == nil {
		clip.Size = info.Size()
	}

	p.addClip(clip)
	p.broadcast(Event{Type: "clip", Clip: &clip})
	return nil
}

// runFile analyses a whole file then exports each detected highlight.
func (p *Pipeline) runFile(ctx context.Context, src source.Source) error {
	info, err := p.tools.Probe(ctx, src.Path)
	if err != nil {
		return err
	}
	if info.Duration <= 0 {
		return fmt.Errorf("could not read duration")
	}
	p.logf("analysing %.1fs of footage", info.Duration)

	analyzer := analyze.New(p.tools)
	detector := analyze.NewDetector(analyze.Options{
		Threshold:    p.cfg.Threshold,
		PreRoll:      p.cfg.PreRoll,
		PostRoll:     p.cfg.PostRoll,
		MinClip:      p.cfg.MinClip,
		MaxClip:      p.cfg.MaxClip,
		Cooldown:     p.cfg.Cooldown,
		BaselineSecs: p.cfg.BaselineSecs,
		HasAudio:     info.HasAudio,
	})

	var highlights []analyze.Highlight
	lastUpdate := time.Now()
	err = analyzer.Run(ctx, src, info.HasAudio, func(s analyze.Sample) {
		if h := detector.Add(s); h != nil {
			highlights = append(highlights, *h)
		}
		if time.Since(lastUpdate) > 500*time.Millisecond {
			lastUpdate = time.Now()
			p.mu.Lock()
			p.status.Score = detector.Normalise(s)
			p.status.Baseline = detector.Baseline()
			p.status.BufferEnd = s.T
			p.mu.Unlock()
			p.broadcast(Event{Type: "status", Status: p.statusPtr()})
		}
	})
	if err != nil {
		return err
	}
	if h := detector.Flush(); h != nil {
		highlights = append(highlights, *h)
	}

	p.mu.Lock()
	p.status.Highlights = len(highlights)
	p.mu.Unlock()
	p.logf("found %d highlight(s)", len(highlights))
	if len(highlights) == 0 {
		return nil
	}

	for i, h := range highlights {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		end := h.End
		if end > info.Duration {
			end = info.Duration
		}
		if end-h.Start < 1 {
			continue
		}
		work, err := os.MkdirTemp(p.cfg.OutputDir, "work-")
		if err != nil {
			return err
		}
		err = p.export(ctx, src.Path, h.Start, end, work, h)
		os.RemoveAll(work)
		if err != nil {
			p.logf("clip %d failed: %v", i+1, err)
			continue
		}
	}
	return nil
}

func (p *Pipeline) newClipMeta(h analyze.Highlight) Clip {
	p.mu.Lock()
	p.seq++
	seq := p.seq
	p.mu.Unlock()
	now := time.Now()
	return Clip{
		ID:        fmt.Sprintf("%d-%d", now.Unix(), seq),
		File:      fmt.Sprintf("clip_%s_%02d.mp4", now.Format("20060102_150405"), seq),
		Start:     h.Start,
		End:       h.End,
		CreatedAt: now,
	}
}

func (p *Pipeline) addClip(c Clip) {
	p.mu.Lock()
	p.clips = append([]Clip{c}, p.clips...)
	p.status.ClipCount = len(p.clips)
	snapshot := make([]Clip, len(p.clips))
	copy(snapshot, p.clips)
	p.mu.Unlock()
	p.saveIndex(snapshot)
}

type indexFile struct {
	Clips []Clip `json:"clips"`
}

func (p *Pipeline) indexPath() string {
	return filepath.Join(p.cfg.OutputDir, "clips.json")
}

func (p *Pipeline) loadIndex() {
	data, err := os.ReadFile(p.indexPath())
	if err != nil {
		return
	}
	var idx indexFile
	if json.Unmarshal(data, &idx) != nil {
		return
	}
	kept := idx.Clips[:0]
	for _, c := range idx.Clips {
		if _, err := os.Stat(filepath.Join(p.cfg.OutputDir, c.File)); err == nil {
			kept = append(kept, c)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].CreatedAt.After(kept[j].CreatedAt) })
	p.clips = kept
}

func (p *Pipeline) saveIndex(clips []Clip) {
	data, err := json.MarshalIndent(indexFile{Clips: clips}, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(p.indexPath(), append(data, '\n'), 0o644)
}

func (p *Pipeline) statusPtr() *Status {
	s := p.Status()
	return &s
}
