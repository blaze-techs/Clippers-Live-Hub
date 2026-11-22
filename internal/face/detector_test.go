package face

import "testing"

func TestDetectGrayRejectsBadInput(t *testing.T) {
	d := NewDetector()
	if got := d.DetectGray(nil, 0, 0); got != nil {
		t.Errorf("empty input should return nothing, got %v", got)
	}
	if got := d.DetectGray([]byte{1, 2, 3}, 100, 100); got != nil {
		t.Errorf("short buffer should return nothing, got %v", got)
	}
}

func TestDetectGrayFindsNothingInFlatImage(t *testing.T) {
	d := NewDetector()
	pixels := make([]byte, 160*120)
	for i := range pixels {
		pixels[i] = 128
	}
	if got := d.DetectGray(pixels, 160, 120); len(got) != 0 {
		t.Errorf("flat image should yield no faces, got %d", len(got))
	}
}

func TestLargestPicksBiggestBox(t *testing.T) {
	rects := []Rect{
		{X: 0, Y: 0, W: 20, H: 20},
		{X: 10, Y: 10, W: 80, H: 80},
		{X: 50, Y: 50, W: 40, H: 40},
	}
	big := Largest(rects)
	if big == nil || big.W != 80 {
		t.Fatalf("expected the 80px box, got %+v", big)
	}
	if Largest(nil) != nil {
		t.Error("nil input should return nil")
	}
}

func TestRectCenter(t *testing.T) {
	r := Rect{X: 10, Y: 20, W: 100, H: 60}
	if r.CenterX() != 60 || r.CenterY() != 50 {
		t.Errorf("centres (%.0f,%.0f), want (60,50)", r.CenterX(), r.CenterY())
	}
	if r.Area() != 6000 {
		t.Errorf("area %.0f, want 6000", r.Area())
	}
}
