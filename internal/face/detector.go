// Package face provides pure-Go face detection for subject tracking.
package face

import (
	"sort"

	pigo "github.com/esimov/pigo/pkg"
)

// Rect is a detection box in pixel coordinates.
type Rect struct {
	X, Y, W, H float64
}

// CenterX returns the horizontal midpoint.
func (r Rect) CenterX() float64 { return r.X + r.W/2 }

// CenterY returns the vertical midpoint.
func (r Rect) CenterY() float64 { return r.Y + r.H/2 }

// Area returns the box area.
func (r Rect) Area() float64 { return r.W * r.H }

// Detector wraps the pigo cascade.
type Detector struct {
	p                *pigo.Pigo
	MinSize, MaxSize int
	Shift, Scale     float64
}

// NewDetector loads the embedded frontal-face cascade.
func NewDetector() *Detector {
	return &Detector{
		p:       pigo.NewPigo().Unpack(),
		MinSize: 20,
		MaxSize: 1200,
		Shift:   0.1,
		Scale:   1.1,
	}
}

// DetectGray finds faces in a grayscale buffer of w*h bytes.
func (d *Detector) DetectGray(pixels []byte, w, h int) []Rect {
	if w <= 0 || h <= 0 || len(pixels) < w*h {
		return nil
	}
	params := pigo.CascadeParams{
		ImageParams: pigo.ImageParams{Pixels: pixels, Rows: h, Cols: w, Dim: w},
		MinSize:     d.MinSize,
		MaxSize:     d.MaxSize,
		ShiftFactor: d.Shift,
		ScaleFactor: d.Scale,
	}
	detections := d.p.ClusterDetections(d.p.RunCascade(params, 0.0), 0.2)
	out := make([]Rect, 0, len(detections))
	for _, det := range detections {
		if det.Q < 5 {
			continue
		}
		out = append(out, Rect{
			X: float64(det.Col) - float64(det.Scale)/2,
			Y: float64(det.Row) - float64(det.Scale)/2,
			W: float64(det.Scale),
			H: float64(det.Scale),
		})
	}
	return out
}

// Largest returns the biggest detection, or nil when none were found.
func Largest(rects []Rect) *Rect {
	if len(rects) == 0 {
		return nil
	}
	sorted := make([]Rect, len(rects))
	copy(sorted, rects)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Area() > sorted[j].Area() })
	return &sorted[0]
}
