package analyze

import (
	"math"
	"testing"
)

// timeline builds samples for a quiet stream with loud bursts at the given
// [start,end) ranges.
func timeline(dur float64, bursts [][2]float64) []Sample {
	var out []Sample
	for t := 0.0; t < dur; t += 0.1 {
		rms := -46.0
		for _, b := range bursts {
			if t >= b[0] && t < b[1] {
				rms = -6.0
			}
		}
		out = append(out, Sample{T: t, RMS: rms})
	}
	return out
}

func newTestDetector() *Detector {
	return NewDetector(Options{
		Threshold:    0.55,
		PreRoll:      6,
		PostRoll:     4,
		MinClip:      6,
		MaxClip:      20,
		Cooldown:     8,
		BaselineSecs: 30,
		HasAudio:     true,
	})
}

func TestDetectsLoudMoment(t *testing.T) {
	d := newTestDetector()
	var got []Highlight
	for _, s := range timeline(60, [][2]float64{{20, 23}}) {
		if h := d.Add(s); h != nil {
			got = append(got, *h)
		}
	}
	if h := d.Flush(); h != nil {
		got = append(got, *h)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 highlight, got %d: %+v", len(got), got)
	}
	h := got[0]
	if math.Abs(h.PeakTime-20) > 0.5 {
		t.Errorf("peak time %.2f, want ~20", h.PeakTime)
	}
	if math.Abs(h.Start-14) > 0.6 {
		t.Errorf("start %.2f, want ~14 (peak - preroll)", h.Start)
	}
	if math.Abs(h.End-24) > 0.8 {
		t.Errorf("end %.2f, want ~24 (peak + postroll)", h.End)
	}
}

func TestSteadyLoudStreamProducesNothing(t *testing.T) {
	d := newTestDetector()
	var got []Highlight
	for t := 0.0; t < 60; t += 0.1 {
		if h := d.Add(Sample{T: t, RMS: -12}); h != nil {
			got = append(got, *h)
		}
	}
	if len(got) != 0 {
		t.Fatalf("constant loudness should not trigger, got %d highlights", len(got))
	}
}

func TestCooldownSuppressesRepeats(t *testing.T) {
	d := newTestDetector()
	var got []Highlight
	for _, s := range timeline(60, [][2]float64{{20, 21}, {22, 23}}) {
		if h := d.Add(s); h != nil {
			got = append(got, *h)
		}
	}
	if len(got) != 1 {
		t.Fatalf("cooldown should merge close spikes, got %d: %+v", len(got), got)
	}
}

func TestTwoSeparatedMomentsProduceTwoClips(t *testing.T) {
	d := newTestDetector()
	var got []Highlight
	for _, s := range timeline(60, [][2]float64{{15, 17}, {45, 47}}) {
		if h := d.Add(s); h != nil {
			got = append(got, *h)
		}
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 highlights, got %d: %+v", len(got), got)
	}
}

func TestSceneOnlyDetectionWithoutAudio(t *testing.T) {
	d := NewDetector(Options{Threshold: 0.55, PreRoll: 5, PostRoll: 3, MinClip: 5, MaxClip: 20, Cooldown: 5, BaselineSecs: 30, HasAudio: false})
	var got []Highlight
	for t := 0.0; t < 30; t += 0.1 {
		scene := 0.0
		if t >= 10 && t < 12 {
			scene = 0.9
		}
		if h := d.Add(Sample{T: t, RMS: -90, Scene: scene}); h != nil {
			got = append(got, *h)
		}
	}
	if len(got) != 1 {
		t.Fatalf("expected a visual-only highlight, got %d", len(got))
	}
}

func TestClipLengthIsCapped(t *testing.T) {
	d := NewDetector(Options{Threshold: 0.55, PreRoll: 30, PostRoll: 30, MinClip: 5, MaxClip: 20, Cooldown: 1, BaselineSecs: 10, HasAudio: true})
	var got []Highlight
	for _, s := range timeline(60, [][2]float64{{25, 26}}) {
		if h := d.Add(s); h != nil {
			got = append(got, *h)
		}
	}
	if len(got) == 0 {
		t.Fatal("expected a highlight")
	}
	if got[0].End-got[0].Start > 20.001 {
		t.Fatalf("clip length %.2f exceeds cap", got[0].End-got[0].Start)
	}
}

func TestParserExtractsSamples(t *testing.T) {
	p := NewParser(true)
	lines := []string{
		"[Parsed_ametadata_2 @ 0x55] frame:0    pts:0       pts_time:0",
		"[Parsed_ametadata_2 @ 0x55] lavfi.astats.Overall.RMS_level=-31.5",
		"[Parsed_ametadata_2 @ 0x55] frame:1    pts:1024    pts_time:0.021",
		"[Parsed_ametadata_2 @ 0x55] lavfi.astats.Overall.RMS_level=-inf",
		"[Parsed_metadata_3 @ 0x55] frame:7    pts:0       pts_time:0.233",
		"[Parsed_metadata_3 @ 0x55] lavfi.scene_score=0.42",
		"[Parsed_ametadata_2 @ 0x55] frame:2    pts:2048    pts_time:0.042",
		"[Parsed_ametadata_2 @ 0x55] lavfi.astats.Overall.RMS_level=-20.0",
	}
	var samples []Sample
	for _, l := range lines {
		if s, ok := p.Line(l); ok {
			samples = append(samples, s)
		}
	}
	if len(samples) != 3 {
		t.Fatalf("expected 3 samples, got %d: %+v", len(samples), samples)
	}
	if samples[0].RMS != -31.5 {
		t.Errorf("first RMS %.2f", samples[0].RMS)
	}
	if samples[1].RMS != -90 {
		t.Errorf("silence should clamp to -90, got %.2f", samples[1].RMS)
	}
	if math.Abs(samples[2].T-0.042) > 1e-6 {
		t.Errorf("third sample time %.3f", samples[2].T)
	}
	if math.Abs(samples[2].Scene-0.42) > 1e-6 {
		t.Errorf("scene score should attach to the next audio sample, got %.2f", samples[2].Scene)
	}
}
