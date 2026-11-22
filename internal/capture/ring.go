// Package capture maintains a rolling buffer of encoded segments.
package capture

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Segment is one finished piece of the rolling buffer.
type Segment struct {
	Path  string
	Start float64
	End   float64
}

// Ring is a time-bounded, on-disk buffer of media segments.
type Ring struct {
	dir      string
	segTime  float64
	keepSecs float64

	mu   sync.RWMutex
	segs []Segment
}

// NewRing prepares a buffer directory.
func NewRing(dir string, segmentSeconds, keepSeconds float64) (*Ring, error) {
	if segmentSeconds <= 0 {
		segmentSeconds = 2
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Ring{dir: dir, segTime: segmentSeconds, keepSecs: keepSeconds}, nil
}

// Dir returns the buffer directory.
func (r *Ring) Dir() string { return r.dir }

// Segments returns a snapshot of retained segments, ordered by start time.
func (r *Ring) Segments() []Segment {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Segment, len(r.segs))
	copy(out, r.segs)
	return out
}

// Span returns the buffered media range.
func (r *Ring) Span() (start, end float64) {
	segs := r.Segments()
	if len(segs) == 0 {
		return 0, 0
	}
	return segs[0].Start, segs[len(segs)-1].End
}

// Cover returns the segments overlapping [from, to], in order.
func (r *Ring) Cover(from, to float64) []Segment {
	segs := r.Segments()
	var out []Segment
	for _, s := range segs {
		if s.End <= from || s.Start >= to {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Truncate drops segments older than keepSecs behind the newest one.
func (r *Ring) Truncate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.segs) == 0 {
		return
	}
	newest := r.segs[len(r.segs)-1].End
	cutoff := newest - r.keepSecs
	keep := r.segs[:0]
	for _, s := range r.segs {
		if s.End < cutoff {
			os.Remove(s.Path)
			continue
		}
		keep = append(keep, s)
	}
	r.segs = keep
}

// Reset clears all buffered segments.
func (r *Ring) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.segs {
		os.Remove(s.Path)
	}
	r.segs = nil
}

func (r *Ring) add(s Segment) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.segs {
		if existing.Path == s.Path {
			return
		}
	}
	r.segs = append(r.segs, s)
	sort.Slice(r.segs, func(i, j int) bool { return r.segs[i].Start < r.segs[j].Start })
}

func parseCSV(line, dir string) (Segment, bool) {
	parts := strings.Split(strings.TrimSpace(line), ",")
	if len(parts) < 3 {
		return Segment{}, false
	}
	start, err1 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	end, err2 := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	if err1 != nil || err2 != nil {
		return Segment{}, false
	}
	name := strings.TrimSpace(parts[0])
	if !filepath.IsAbs(name) {
		name = filepath.Join(dir, filepath.Base(name))
	}
	return Segment{Path: name, Start: start, End: end}, true
}

// Describe renders a segment list for debugging.
func Describe(segs []Segment) string {
	var b strings.Builder
	for _, s := range segs {
		fmt.Fprintf(&b, "%s [%.2f-%.2f] ", filepath.Base(s.Path), s.Start, s.End)
	}
	return strings.TrimSpace(b.String())
}
