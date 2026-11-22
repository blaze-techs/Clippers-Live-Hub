package reframe

import (
	"math"
	"strings"
	"testing"
)

func TestComputeNineBySixteenCrop(t *testing.T) {
	g := Compute(1920, 1080, 1080, 1920)
	if g.CropH != 1080 {
		t.Errorf("crop height %d, want 1080", g.CropH)
	}
	want := int(math.Round(1080 * 9.0 / 16.0))
	if g.CropW != want {
		t.Errorf("crop width %d, want %d", g.CropW, want)
	}
	if g.CropW%2 != 0 || g.CropH%2 != 0 {
		t.Errorf("crop must be even for yuv420p, got %dx%d", g.CropW, g.CropH)
	}
}

func TestComputeNarrowSource(t *testing.T) {
	g := Compute(720, 1280, 1080, 1920)
	if g.CropW != 720 {
		t.Errorf("narrow source should use full width, got %d", g.CropW)
	}
	if g.CropH > 1280 {
		t.Errorf("crop height %d exceeds source", g.CropH)
	}
}

func TestOffsetClampsToFrame(t *testing.T) {
	g := Compute(1920, 1080, 1080, 1920)
	if x := OffsetX(0.0, g); x != 0 {
		t.Errorf("left edge offset %d, want 0", x)
	}
	if x := OffsetX(1.0, g); x != 1920-g.CropW {
		t.Errorf("right edge offset %d, want %d", x, 1920-g.CropW)
	}
	if y := OffsetY(0.5, g); y != 0 {
		t.Errorf("vertical centre offset %d, want 0", y)
	}
}

func TestSmoothReducesJitter(t *testing.T) {
	raw := []Point{
		{T: 0, X: 0.50, Y: 0.5},
		{T: 0.25, X: 0.62, Y: 0.5},
		{T: 0.50, X: 0.38, Y: 0.5},
		{T: 0.75, X: 0.60, Y: 0.5},
	}
	out := Smooth(raw, 0.2, 0)
	if len(out) != len(raw) {
		t.Fatalf("length changed")
	}
	span := func(ps []Point) float64 {
		min, max := 1.0, 0.0
		for _, p := range ps {
			min = math.Min(min, p.X)
			max = math.Max(max, p.X)
		}
		return max - min
	}
	if span(out) >= span(raw) {
		t.Errorf("smoothing did not reduce spread: raw %.3f smooth %.3f", span(raw), span(out))
	}
}

func TestBuildPathHoldsLastPositionWhenLost(t *testing.T) {
	g := Compute(1920, 1080, 1080, 1920)
	track := []Point{
		{T: 0, X: 0.3, Y: 0.5},
		{T: 0.25, X: 0, Y: 0}, // detector lost the face
		{T: 0.5, X: 0, Y: 0},
	}
	out := BuildPath(track, g, 1.0, 0)
	if math.Abs(out[1].X-0.3) > 1e-9 {
		t.Errorf("lost frame should hold previous x, got %.3f", out[1].X)
	}
}

func TestSendCmdFormat(t *testing.T) {
	g := Compute(1920, 1080, 1080, 1920)
	path := []Point{{T: 0, X: 0.5, Y: 0.5}, {T: 1.0, X: 0.8, Y: 0.5}}
	script := SendCmd(path, g)
	if !strings.Contains(script, "crop x") {
		t.Fatalf("missing crop command: %q", script)
	}
	lines := strings.Split(strings.TrimSpace(script), "\n")
	for _, l := range lines {
		if !strings.HasSuffix(l, ";") {
			t.Errorf("command must end with semicolon: %q", l)
		}
	}
}

func TestFilterChainUsesSendCmdWhenPathPresent(t *testing.T) {
	g := Compute(1920, 1080, 1080, 1920)
	chain := FilterChain("face", g, "/tmp/crop.cmd", 1080, 1920)
	if !strings.HasPrefix(chain, "sendcmd=") {
		t.Errorf("expected sendcmd chain, got %q", chain)
	}
	if !strings.Contains(chain, "scale=1080:1920") {
		t.Errorf("missing scale: %q", chain)
	}
	center := FilterChain("center", g, "", 1080, 1920)
	if strings.Contains(center, "sendcmd") {
		t.Errorf("center layout should not animate: %q", center)
	}
}
