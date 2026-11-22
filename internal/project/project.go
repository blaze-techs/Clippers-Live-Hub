// Package project holds the portable project format: media references,
// tracks, clips, caption cues and export settings.
//
// Everything is plain JSON so a project written on Windows opens on Linux and
// can be inspected or repaired by hand.
package project

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Track kinds.
const (
	TrackVideo   = "video"
	TrackAudio   = "audio"
	TrackCaption = "caption"
	TrackOverlay = "overlay"
)

// Project is the root document.
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Width and Height are the canvas size that clips are composited onto.
	Width  int     `json:"width"`
	Height int     `json:"height"`
	FPS    float64 `json:"fps"`

	Media  []Media `json:"media"`
	Tracks []Track `json:"tracks"`
	Cues   []Cue   `json:"cues"`
	Style  Style   `json:"caption_style"`
	Export Export  `json:"export"`

	// Dir is where this project's sidecar assets live. Not serialised.
	Dir string `json:"-"`
}

// Media is one imported source.
type Media struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	RelPath  string    `json:"rel_path,omitempty"`
	Duration float64   `json:"duration"`
	Width    int       `json:"width"`
	Height   int       `json:"height"`
	FPS      float64   `json:"fps"`
	HasAudio bool      `json:"has_audio"`
	HasVideo bool      `json:"has_video"`
	Thumbs   []string  `json:"thumbs,omitempty"`
	Peaks    []float64 `json:"peaks,omitempty"`
	AddedAt  time.Time `json:"added_at"`
}

// Track is a lane on the timeline.
type Track struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Hidden bool   `json:"hidden,omitempty"`
	Muted  bool   `json:"muted,omitempty"`
	Locked bool   `json:"locked,omitempty"`
	Clips  []Clip `json:"clips"`
}

// Clip is one placement of media on a track.
type Clip struct {
	ID      string  `json:"id"`
	MediaID string  `json:"media_id"`
	Name    string  `json:"name"`
	Start   float64 `json:"start"` // timeline position, seconds
	In      float64 `json:"in"`    // source in point
	Out     float64 `json:"out"`   // source out point

	// Crop is the normalised source rectangle to keep. Nil means "fit the
	// canvas", which is what an untouched import uses.
	Crop *Rect `json:"crop,omitempty"`
	// Keyframes override Crop from a given clip-local time onwards.
	Keyframes []Keyframe `json:"keyframes,omitempty"`
	// PiP places the clip as a picture-in-picture rectangle on the canvas.
	PiP *Rect `json:"pip,omitempty"`

	Volume  float64 `json:"volume,omitempty"`
	FadeIn  float64 `json:"fade_in,omitempty"`
	FadeOut float64 `json:"fade_out,omitempty"`

	// Highlight carries why the detector proposed this clip.
	Highlight *Highlight `json:"highlight,omitempty"`
}

// Highlight is the detector's justification for a clip.
type Highlight struct {
	Score      float64 `json:"score"`
	Reason     string  `json:"reason"`
	PeakAt     float64 `json:"peak_at"`
	LoudnessDB float64 `json:"loudness_db"`
	Activity   float64 `json:"activity"`
}

// Keyframe animates the crop from its time onward.
type Keyframe struct {
	T    float64 `json:"t"` // seconds from clip start
	Rect Rect    `json:"rect"`
}

// Rect is a normalised rectangle; all values are 0..1 fractions.
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Cue is one caption line.
type Cue struct {
	ID    string  `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// Style is the burned-in caption look.
type Style struct {
	Font      string `json:"font"`
	Size      int    `json:"size"`
	Color     string `json:"color"`   // #RRGGBB
	Outline   string `json:"outline"` // #RRGGBB
	OutlineW  int    `json:"outline_w"`
	Bold      bool   `json:"bold"`
	Upper     bool   `json:"upper"`
	MarginV   int    `json:"margin_v"`  // percent from the bottom
	Highlight string `json:"highlight"` // #RRGGBB
}

// Export is the render configuration.
type Export struct {
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	FPS       float64 `json:"fps"`
	VBitrate  string  `json:"v_bitrate"`
	ABitrate  string  `json:"a_bitrate"`
	Preset    string  `json:"preset"`
	CRF       int     `json:"crf"`
	Clean     bool    `json:"clean_audio"`
	Burn      bool    `json:"burn_captions"`
	Watermark string  `json:"watermark,omitempty"`
}

// New returns an empty project with sensible defaults.
func New(name string) *Project {
	now := time.Now().UTC()
	return &Project{
		ID:        NewID(),
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
		Width:     1080,
		Height:    1920,
		FPS:       30,
		Media:     []Media{},
		Tracks: []Track{
			{ID: NewID(), Kind: TrackVideo, Name: "Video 1"},
			{ID: NewID(), Kind: TrackOverlay, Name: "Overlay"},
			{ID: NewID(), Kind: TrackAudio, Name: "Audio"},
			{ID: NewID(), Kind: TrackCaption, Name: "Captions"},
		},
		Cues:  []Cue{},
		Style: DefaultStyle(),
		Export: Export{
			Width: 1080, Height: 1920, FPS: 30,
			VBitrate: "8M", ABitrate: "192k", Preset: "veryfast", CRF: 20,
			Clean: true, Burn: true,
		},
	}
}

// DefaultStyle is a bold, readable short-form caption look.
func DefaultStyle() Style {
	return Style{
		Font: "DejaVu Sans", Size: 64, Color: "#FFFFFF", Outline: "#000000",
		OutlineW: 4, Bold: true, Upper: true, MarginV: 18, Highlight: "#FF2D8E",
	}
}

// NewID returns a short random identifier.
func NewID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Duration is the end of the last clip on any track.
func (p *Project) Duration() float64 {
	var end float64
	for _, t := range p.Tracks {
		for _, c := range t.Clips {
			if e := c.End(); e > end {
				end = e
			}
		}
	}
	return end
}

// Length is the trimmed source length of the clip.
func (c Clip) Length() float64 {
	if c.Out < c.In {
		return 0
	}
	return c.Out - c.In
}

// End is the timeline end of the clip.
func (c Clip) End() float64 { return c.Start + c.Length() }

// MediaByID looks up an imported source.
func (p *Project) MediaByID(id string) *Media {
	for i := range p.Media {
		if p.Media[i].ID == id {
			return &p.Media[i]
		}
	}
	return nil
}

// TrackByID looks up a track.
func (p *Project) TrackByID(id string) *Track {
	for i := range p.Tracks {
		if p.Tracks[i].ID == id {
			return &p.Tracks[i]
		}
	}
	return nil
}

// FirstVideoTrack returns the base lane, or nil.
func (p *Project) FirstVideoTrack() *Track {
	for i := range p.Tracks {
		if p.Tracks[i].Kind == TrackVideo {
			return &p.Tracks[i]
		}
	}
	return nil
}

// MissingMedia lists sources whose file is gone.
func (p *Project) MissingMedia() []Media {
	var out []Media
	for _, m := range p.Media {
		if _, err := os.Stat(p.resolve(m)); err != nil {
			out = append(out, m)
		}
	}
	return out
}

func (p *Project) resolve(m Media) string {
	if m.Path != "" {
		if _, err := os.Stat(m.Path); err == nil {
			return m.Path
		}
	}
	if m.RelPath != "" && p.Dir != "" {
		candidate := filepath.Join(p.Dir, m.RelPath)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return m.Path
}

// Resolve returns a usable path for a media entry, falling back to the
// project-relative copy when the original moved.
func (p *Project) Resolve(m Media) string { return p.resolve(m) }

// Relink points a media entry at a new file.
func (p *Project) Relink(mediaID, path string) error {
	m := p.MediaByID(mediaID)
	if m == nil {
		return fmt.Errorf("media %q not found", mediaID)
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("cannot read %s: %w", path, err)
	}
	m.Path = path
	m.RelPath = ""
	p.Touch()
	return nil
}

// Touch updates the modification time.
func (p *Project) Touch() { p.UpdatedAt = time.Now().UTC() }

// Validate repairs values that would break rendering.
func (p *Project) Validate() {
	if p.Width <= 0 {
		p.Width = 1080
	}
	if p.Height <= 0 {
		p.Height = 1920
	}
	if p.FPS <= 0 {
		p.FPS = 30
	}
	if p.Export.Width <= 0 {
		p.Export.Width = p.Width
	}
	if p.Export.Height <= 0 {
		p.Export.Height = p.Height
	}
	if p.Export.FPS <= 0 {
		p.Export.FPS = p.FPS
	}
	if p.Export.Preset == "" {
		p.Export.Preset = "veryfast"
	}
	if p.Export.CRF <= 0 || p.Export.CRF > 51 {
		p.Export.CRF = 20
	}
	if p.Style.Size <= 0 {
		p.Style = DefaultStyle()
	}
	// Nil slices encode as JSON null, which every client would have to special
	// case. Normalise them to empty arrays once, here.
	if p.Media == nil {
		p.Media = []Media{}
	}
	if p.Tracks == nil {
		p.Tracks = []Track{}
	}
	if p.Cues == nil {
		p.Cues = []Cue{}
	}
	for ti := range p.Tracks {
		if p.Tracks[ti].Clips == nil {
			p.Tracks[ti].Clips = []Clip{}
		}
	}
	for ti := range p.Tracks {
		t := &p.Tracks[ti]
		if t.Kind == "" {
			t.Kind = TrackVideo
		}
		for ci := range t.Clips {
			c := &t.Clips[ci]
			if c.Out <= c.In {
				if m := p.MediaByID(c.MediaID); m != nil {
					c.Out = math.Min(m.Duration, c.In+1)
				}
			}
			if c.Volume == 0 {
				c.Volume = 1
			}
			if c.Crop != nil {
				c.Crop.Clamp()
			}
			if c.PiP != nil {
				c.PiP.Clamp()
			}
			for k := range c.Keyframes {
				c.Keyframes[k].Rect.Clamp()
			}
		}
	}
	p.SortCues()
}

// Clamp keeps a rectangle inside the unit square.
func (r *Rect) Clamp() {
	r.W = clampRange(r.W, 0.02, 1)
	r.H = clampRange(r.H, 0.02, 1)
	r.X = clampRange(r.X, 0, 1-r.W)
	r.Y = clampRange(r.Y, 0, 1-r.H)
}

func clampRange(v, lo, hi float64) float64 {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// SortCues orders captions by start time.
func (p *Project) SortCues() {
	for i := 1; i < len(p.Cues); i++ {
		for j := i; j > 0 && p.Cues[j].Start < p.Cues[j-1].Start; j-- {
			p.Cues[j], p.Cues[j-1] = p.Cues[j-1], p.Cues[j]
		}
	}
}

// Marshal encodes the project document.
func (p *Project) Marshal() ([]byte, error) {
	p.Validate()
	return json.MarshalIndent(p, "", "  ")
}

// Unmarshal decodes a project document.
func Unmarshal(data []byte) (*Project, error) {
	var p Project
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	p.Validate()
	return &p, nil
}

// Sanitise makes a project name safe for a directory name.
func Sanitise(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return "project"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "project"
	}
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}
