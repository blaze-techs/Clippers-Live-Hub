package project

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.Create("My First Project")
	if err != nil {
		t.Fatal(err)
	}

	mediaFile := filepath.Join(root, "source.mp4")
	if err := os.WriteFile(mediaFile, []byte("not a real video"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.Media = append(p.Media, Media{
		ID: "m1", Name: "source.mp4", Path: mediaFile, Duration: 12.5,
		Width: 1920, Height: 1080, HasVideo: true, HasAudio: true,
	})

	ed := NewEditor(p)
	if err := ed.AddClip(p.FirstVideoTrack().ID, Clip{
		MediaID: "m1", Start: 1.5, In: 2, Out: 8,
		Crop: &Rect{X: 0.25, Y: 0, W: 0.5, H: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ed.AddCue(0.5, 2.5, "first line"); err != nil {
		t.Fatal(err)
	}

	if err := store.Save(ed.P); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load(ed.P.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "My First Project" {
		t.Errorf("name = %q", loaded.Name)
	}
	if len(loaded.Media) != 1 || loaded.Media[0].Duration != 12.5 {
		t.Fatalf("media did not round-trip: %+v", loaded.Media)
	}
	if len(loaded.Cues) != 1 || loaded.Cues[0].Text != "first line" {
		t.Fatalf("cues did not round-trip: %+v", loaded.Cues)
	}
	vt := loaded.FirstVideoTrack()
	if len(vt.Clips) != 1 {
		t.Fatalf("expected 1 clip, got %d", len(vt.Clips))
	}
	c := vt.Clips[0]
	if c.Start != 1.5 || c.In != 2 || c.Out != 8 {
		t.Errorf("clip timing lost: %+v", c)
	}
	if c.Crop == nil || math.Abs(c.Crop.W-0.5) > 1e-9 {
		t.Errorf("crop lost: %+v", c.Crop)
	}
	if got := loaded.Duration(); math.Abs(got-7.5) > 1e-6 {
		t.Errorf("duration = %.3f, want 7.5", got)
	}
}

func TestRecoversInterruptedSave(t *testing.T) {
	root := t.TempDir()
	store, _ := NewStore(root)
	p, _ := store.Create("recover me")

	data, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash: a complete temp file, no main document.
	if err := os.Remove(filepath.Join(store.Dir(p.ID), "project.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir(p.ID), "project.json.tmp"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := store.Load(p.ID)
	if err != nil {
		t.Fatalf("expected recovery, got %v", err)
	}
	if got.Name != "recover me" {
		t.Errorf("recovered wrong project: %q", got.Name)
	}
}

func TestUndoRedo(t *testing.T) {
	p := New("undo")
	ed := NewEditor(p)
	track := p.FirstVideoTrack().ID
	p.Media = append(p.Media, Media{ID: "m1", Duration: 10, HasVideo: true})

	if err := ed.AddClip(track, Clip{MediaID: "m1", Start: 0, In: 0, Out: 5}); err != nil {
		t.Fatal(err)
	}
	ed.P = p
	if len(ed.P.FirstVideoTrack().Clips) != 1 {
		t.Fatalf("clip not added")
	}

	if !ed.Undo() {
		t.Fatal("undo returned false")
	}
	if n := len(ed.P.FirstVideoTrack().Clips); n != 0 {
		t.Fatalf("after undo expected 0 clips, got %d", n)
	}
	if !ed.Redo() {
		t.Fatal("redo returned false")
	}
	if n := len(ed.P.FirstVideoTrack().Clips); n != 1 {
		t.Fatalf("after redo expected 1 clip, got %d", n)
	}
}

func TestSplitAndTrim(t *testing.T) {
	p := New("split")
	ed := NewEditor(p)
	track := p.FirstVideoTrack().ID
	p.Media = append(p.Media, Media{ID: "m1", Duration: 20, HasVideo: true})

	if err := ed.AddClip(track, Clip{MediaID: "m1", Start: 0, In: 0, Out: 10}); err != nil {
		t.Fatal(err)
	}
	id := ed.P.FirstVideoTrack().Clips[0].ID
	rightID, err := ed.SplitClip(id, 4)
	if err != nil {
		t.Fatal(err)
	}
	clips := ed.P.FirstVideoTrack().Clips
	if len(clips) != 2 {
		t.Fatalf("expected 2 clips after split, got %d", len(clips))
	}
	if math.Abs(clips[0].Out-4) > 1e-9 {
		t.Errorf("left clip out = %.3f, want 4", clips[0].Out)
	}
	var right *Clip
	for i := range clips {
		if clips[i].ID == rightID {
			right = &clips[i]
		}
	}
	if right == nil {
		t.Fatal("right clip missing")
	}
	if math.Abs(right.Start-4) > 1e-9 || math.Abs(right.In-4) > 1e-9 {
		t.Errorf("right clip not contiguous: start=%.3f in=%.3f", right.Start, right.In)
	}

	if err := ed.TrimClip(rightID, 4, 4, 6); err != nil {
		t.Fatal(err)
	}
	if err := ed.TrimClip(rightID, 4, 4, 40); err != nil {
		t.Fatalf("trim past media duration should clamp, not fail: %v", err)
	}
	_, c := ed.P.FindClip(rightID)
	if math.Abs(c.Out-20) > 1e-9 {
		t.Errorf("out point should clamp to media duration 20, got %.2f", c.Out)
	}
}

func TestMissingMediaAndRelink(t *testing.T) {
	p := New("missing")
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone.mp4")
	p.Media = append(p.Media, Media{ID: "m1", Path: gone, Duration: 5})

	if missing := p.MissingMedia(); len(missing) != 1 {
		t.Fatalf("expected 1 missing media, got %d", len(missing))
	}

	real := filepath.Join(dir, "moved.mp4")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.Relink("m1", real); err != nil {
		t.Fatal(err)
	}
	if missing := p.MissingMedia(); len(missing) != 0 {
		t.Fatalf("expected media to be found after relink")
	}
}

func TestSRTRoundTrip(t *testing.T) {
	cues := []Cue{
		{ID: "a", Start: 0.5, End: 2.25, Text: "first line"},
		{ID: "b", Start: 3, End: 4.75, Text: "second line"},
	}
	srt := FormatSRT(cues)
	if !strings.Contains(srt, "00:00:00,500 --> 00:00:02,250") {
		t.Fatalf("unexpected SRT:\n%s", srt)
	}
	parsed := ParseSRT(srt)
	if len(parsed) != 2 {
		t.Fatalf("expected 2 cues, got %d", len(parsed))
	}
	if math.Abs(parsed[0].Start-0.5) > 0.001 || math.Abs(parsed[1].End-4.75) > 0.001 {
		t.Errorf("timing drifted: %+v", parsed)
	}
	if parsed[1].Text != "second line" {
		t.Errorf("text = %q", parsed[1].Text)
	}
}

func TestParseSRTWithCRLFAndMissingIndex(t *testing.T) {
	data := "00:00:01,000 --> 00:00:02,000\r\nhello\r\n\r\n2\r\n00:00:03,000 --> 00:00:04,000\r\nworld\r\n"
	cues := ParseSRT(data)
	if len(cues) != 2 {
		t.Fatalf("expected 2 cues, got %d: %+v", len(cues), cues)
	}
	if cues[0].Text != "hello" || cues[1].Text != "world" {
		t.Errorf("text parse: %+v", cues)
	}
}

func TestASSUsesProjectCanvasAndUppercases(t *testing.T) {
	p := New("ass")
	p.Width, p.Height = 1080, 1920
	p.Style.Upper = true
	p.Cues = []Cue{{ID: "a", Start: 0, End: 1.5, Text: "make it loud"}}

	ass := BuildASS(p)
	if !strings.Contains(ass, "PlayResX: 1080") || !strings.Contains(ass, "PlayResY: 1920") {
		t.Error("canvas size missing from ASS header")
	}
	if !strings.Contains(ass, "MAKE IT LOUD") {
		t.Error("uppercase style not applied")
	}
	if !strings.Contains(ass, "Dialogue: 0,0:00:00.00,0:00:01.50") {
		t.Errorf("dialogue timing wrong:\n%s", ass)
	}
}

func TestSnapToClipEdges(t *testing.T) {
	p := New("snap")
	p.Media = append(p.Media, Media{ID: "m1", Duration: 10})
	ed := NewEditor(p)
	if err := ed.AddClip(p.FirstVideoTrack().ID, Clip{MediaID: "m1", Start: 2, In: 0, Out: 3}); err != nil {
		t.Fatal(err)
	}
	if got := ed.P.Snap(5.1, 0.2); math.Abs(got-5) > 1e-9 {
		t.Errorf("expected snap to clip end 5, got %.3f", got)
	}
	if got := ed.P.Snap(7.5, 0.2); math.Abs(got-7.5) > 1e-9 {
		t.Errorf("expected no snap, got %.3f", got)
	}
}
