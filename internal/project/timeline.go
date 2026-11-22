package project

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Editor applies timeline edits and keeps an undo history.
//
// Undo is snapshot based: every mutation stores the encoded project. Documents
// are a few kilobytes, so this is cheaper to get right than inverse operations,
// and it cannot leave a half-applied edit behind.
type Editor struct {
	P     *Project
	undo  [][]byte
	redo  [][]byte
	limit int
}

// NewEditor wraps a project for editing.
func NewEditor(p *Project) *Editor {
	return &Editor{P: p, limit: 100}
}

// CanUndo reports whether a snapshot is available.
func (e *Editor) CanUndo() bool { return len(e.undo) > 0 }

// CanRedo reports whether a redo is available.
func (e *Editor) CanRedo() bool { return len(e.redo) > 0 }

func (e *Editor) snapshot() ([]byte, error) {
	e.P.Validate()
	return json.Marshal(e.P)
}

func (e *Editor) mutate(fn func(*Project) error) error {
	before, err := e.snapshot()
	if err != nil {
		return err
	}
	if err := fn(e.P); err != nil {
		return err
	}
	e.P.Validate()
	e.undo = append(e.undo, before)
	if len(e.undo) > e.limit {
		e.undo = e.undo[len(e.undo)-e.limit:]
	}
	e.redo = nil
	e.P.Touch()
	return nil
}

// Undo restores the previous state.
func (e *Editor) Undo() bool {
	if len(e.undo) == 0 {
		return false
	}
	current, err := e.snapshot()
	if err != nil {
		return false
	}
	prev := e.undo[len(e.undo)-1]
	e.undo = e.undo[:len(e.undo)-1]
	restored, err := Unmarshal(prev)
	if err != nil {
		return false
	}
	restored.Dir = e.P.Dir
	e.P = restored
	e.redo = append(e.redo, current)
	return true
}

// Redo reapplies an undone edit.
func (e *Editor) Redo() bool {
	if len(e.redo) == 0 {
		return false
	}
	current, err := e.snapshot()
	if err != nil {
		return false
	}
	next := e.redo[len(e.redo)-1]
	e.redo = e.redo[:len(e.redo)-1]
	restored, err := Unmarshal(next)
	if err != nil {
		return false
	}
	restored.Dir = e.P.Dir
	e.P = restored
	e.undo = append(e.undo, current)
	return true
}

// FindClip locates a clip and its track.
func (p *Project) FindClip(id string) (*Track, *Clip) {
	for ti := range p.Tracks {
		for ci := range p.Tracks[ti].Clips {
			if p.Tracks[ti].Clips[ci].ID == id {
				return &p.Tracks[ti], &p.Tracks[ti].Clips[ci]
			}
		}
	}
	return nil, nil
}

// AddClip appends a clip to a track.
func (e *Editor) AddClip(trackID string, c Clip) error {
	return e.mutate(func(p *Project) error {
		t := p.TrackByID(trackID)
		if t == nil {
			return fmt.Errorf("track %q not found", trackID)
		}
		if c.ID == "" {
			c.ID = NewID()
		}
		if c.Name == "" {
			if m := p.MediaByID(c.MediaID); m != nil {
				c.Name = m.Name
			}
		}
		if c.Volume == 0 {
			c.Volume = 1
		}
		if c.Out <= c.In {
			m := p.MediaByID(c.MediaID)
			if m == nil {
				return fmt.Errorf("media %q not found", c.MediaID)
			}
			c.Out = m.Duration
		}
		t.Clips = append(t.Clips, c)
		sortClips(t)
		return nil
	})
}

func sortClips(t *Track) {
	sort.SliceStable(t.Clips, func(i, j int) bool { return t.Clips[i].Start < t.Clips[j].Start })
}

// DeleteClip removes a clip.
func (e *Editor) DeleteClip(clipID string) error {
	return e.mutate(func(p *Project) error {
		t, c := p.FindClip(clipID)
		if c == nil {
			return fmt.Errorf("clip %q not found", clipID)
		}
		out := t.Clips[:0]
		for _, x := range t.Clips {
			if x.ID != clipID {
				out = append(out, x)
			}
		}
		t.Clips = out
		return nil
	})
}

// DuplicateClip copies a clip directly after itself.
func (e *Editor) DuplicateClip(clipID string) (string, error) {
	var newID string
	err := e.mutate(func(p *Project) error {
		t, c := p.FindClip(clipID)
		if c == nil {
			return fmt.Errorf("clip %q not found", clipID)
		}
		dup := *c
		dup.ID = NewID()
		dup.Start = c.End()
		newID = dup.ID
		t.Clips = append(t.Clips, dup)
		sortClips(t)
		return nil
	})
	return newID, err
}

// MoveClip repositions a clip, optionally onto another track.
func (e *Editor) MoveClip(clipID, trackID string, start float64) error {
	return e.mutate(func(p *Project) error {
		from, c := p.FindClip(clipID)
		if c == nil {
			return fmt.Errorf("clip %q not found", clipID)
		}
		to := from
		if trackID != "" && trackID != from.ID {
			t := p.TrackByID(trackID)
			if t == nil {
				return fmt.Errorf("track %q not found", trackID)
			}
			to = t
		}
		moved := *c
		moved.Start = math.Max(0, start)
		removeClip(from, clipID)
		to.Clips = append(to.Clips, moved)
		sortClips(to)
		return nil
	})
}

func removeClip(t *Track, id string) {
	out := t.Clips[:0]
	for _, x := range t.Clips {
		if x.ID != id {
			out = append(out, x)
		}
	}
	t.Clips = out
}

// TrimClip sets new source in/out and timeline start for a clip.
func (e *Editor) TrimClip(clipID string, start, in, out float64) error {
	return e.mutate(func(p *Project) error {
		_, c := p.FindClip(clipID)
		if c == nil {
			return fmt.Errorf("clip %q not found", clipID)
		}
		m := p.MediaByID(c.MediaID)
		if m == nil {
			return fmt.Errorf("media %q not found", c.MediaID)
		}
		in = math.Max(0, in)
		out = math.Min(m.Duration, out)
		if out-in < 0.1 {
			return fmt.Errorf("clip must be at least 0.1s")
		}
		c.Start = math.Max(0, start)
		c.In, c.Out = in, out
		return nil
	})
}

// SplitClip cuts a clip at an absolute timeline position.
func (e *Editor) SplitClip(clipID string, at float64) (string, error) {
	var rightID string
	err := e.mutate(func(p *Project) error {
		t, c := p.FindClip(clipID)
		if c == nil {
			return fmt.Errorf("clip %q not found", clipID)
		}
		if at <= c.Start+0.05 || at >= c.End()-0.05 {
			return fmt.Errorf("split point must be inside the clip")
		}
		leftLen := at - c.Start
		right := *c
		right.ID = NewID()
		right.Start = at
		right.In = c.In + leftLen
		c.Out = c.In + leftLen
		rightID = right.ID
		t.Clips = append(t.Clips, right)
		sortClips(t)
		return nil
	})
	return rightID, err
}

// SetCrop replaces a clip's crop rectangle.
func (e *Editor) SetCrop(clipID string, r *Rect) error {
	return e.mutate(func(p *Project) error {
		_, c := p.FindClip(clipID)
		if c == nil {
			return fmt.Errorf("clip %q not found", clipID)
		}
		if r == nil {
			c.Crop = nil
			return nil
		}
		cp := *r
		cp.Clamp()
		c.Crop = &cp
		return nil
	})
}

// SetPiP places or clears the picture-in-picture rectangle.
func (e *Editor) SetPiP(clipID string, r *Rect) error {
	return e.mutate(func(p *Project) error {
		_, c := p.FindClip(clipID)
		if c == nil {
			return fmt.Errorf("clip %q not found", clipID)
		}
		if r == nil {
			c.PiP = nil
			return nil
		}
		cp := *r
		cp.Clamp()
		c.PiP = &cp
		return nil
	})
}

// SetVolume changes a clip's audio gain.
func (e *Editor) SetVolume(clipID string, v float64) error {
	return e.mutate(func(p *Project) error {
		_, c := p.FindClip(clipID)
		if c == nil {
			return fmt.Errorf("clip %q not found", clipID)
		}
		c.Volume = math.Max(0, math.Min(4, v))
		return nil
	})
}

// AddKeyframe animates the crop from time t.
func (e *Editor) AddKeyframe(clipID string, t float64, r Rect) error {
	return e.mutate(func(p *Project) error {
		_, c := p.FindClip(clipID)
		if c == nil {
			return fmt.Errorf("clip %q not found", clipID)
		}
		r.Clamp()
		for i := range c.Keyframes {
			if math.Abs(c.Keyframes[i].T-t) < 0.05 {
				c.Keyframes[i].Rect = r
				return nil
			}
		}
		c.Keyframes = append(c.Keyframes, Keyframe{T: t, Rect: r})
		sort.Slice(c.Keyframes, func(i, j int) bool { return c.Keyframes[i].T < c.Keyframes[j].T })
		return nil
	})
}

// ClearKeyframes removes all animation from a clip.
func (e *Editor) ClearKeyframes(clipID string) error {
	return e.mutate(func(p *Project) error {
		_, c := p.FindClip(clipID)
		if c == nil {
			return fmt.Errorf("clip %q not found", clipID)
		}
		c.Keyframes = nil
		return nil
	})
}

// AddCue appends a caption.
func (e *Editor) AddCue(start, end float64, text string) (string, error) {
	var id string
	err := e.mutate(func(p *Project) error {
		if end <= start {
			return fmt.Errorf("cue end must follow start")
		}
		cue := Cue{ID: NewID(), Start: start, End: end, Text: text}
		id = cue.ID
		p.Cues = append(p.Cues, cue)
		return nil
	})
	return id, err
}

// UpdateCue edits an existing caption.
func (e *Editor) UpdateCue(id string, start, end float64, text string) error {
	return e.mutate(func(p *Project) error {
		for i := range p.Cues {
			if p.Cues[i].ID != id {
				continue
			}
			if end <= start {
				return fmt.Errorf("cue end must follow start")
			}
			p.Cues[i].Start, p.Cues[i].End, p.Cues[i].Text = start, end, text
			return nil
		}
		return fmt.Errorf("cue %q not found", id)
	})
}

// DeleteCue removes a caption.
func (e *Editor) DeleteCue(id string) error {
	return e.mutate(func(p *Project) error {
		out := p.Cues[:0]
		found := false
		for _, c := range p.Cues {
			if c.ID == id {
				found = true
				continue
			}
			out = append(out, c)
		}
		if !found {
			return fmt.Errorf("cue %q not found", id)
		}
		p.Cues = out
		return nil
	})
}

// AddTrack appends a lane of the given kind.
func (e *Editor) AddTrack(kind, name string) error {
	return e.mutate(func(p *Project) error {
		switch kind {
		case TrackVideo, TrackAudio, TrackCaption, TrackOverlay:
		case "":
			kind = TrackVideo
		default:
			return fmt.Errorf("unknown track kind %q", kind)
		}
		if name == "" {
			name = fmt.Sprintf("%s %d", title(kind), len(p.Tracks)+1)
		}
		p.Tracks = append(p.Tracks, Track{ID: NewID(), Kind: kind, Name: name})
		return nil
	})
}

// DeleteTrack removes a lane and everything on it.
func (e *Editor) DeleteTrack(trackID string) error {
	return e.mutate(func(p *Project) error {
		out := p.Tracks[:0]
		found := false
		for _, t := range p.Tracks {
			if t.ID == trackID {
				found = true
				continue
			}
			out = append(out, t)
		}
		if !found {
			return fmt.Errorf("track %q not found", trackID)
		}
		p.Tracks = out
		return nil
	})
}

func title(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Snap returns the nearest interesting edge within threshold, else value.
func (p *Project) Snap(value, threshold float64) float64 {
	best := value
	bestDelta := threshold
	consider := func(v float64) {
		d := math.Abs(v - value)
		if d < bestDelta {
			bestDelta, best = d, v
		}
	}
	consider(0)
	for _, t := range p.Tracks {
		for _, c := range t.Clips {
			consider(c.Start)
			consider(c.End())
		}
	}
	for _, c := range p.Cues {
		consider(c.Start)
		consider(c.End)
	}
	return best
}
