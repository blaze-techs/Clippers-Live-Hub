// Package reframe converts a wide source into a vertical crop that follows the subject.
package reframe

import (
	"fmt"
	"math"
	"strings"
)

// Point is a normalised subject centre (0..1) at a given time.
type Point struct {
	T float64
	X float64
	Y float64
}

// Geometry describes the source crop that fills the output aspect.
type Geometry struct {
	SrcW, SrcH   int
	CropW, CropH int
}

// Compute derives the largest centred crop that matches the output aspect.
func Compute(srcW, srcH, outW, outH int) Geometry {
	g := Geometry{SrcW: srcW, SrcH: srcH}
	target := float64(outW) / float64(outH)
	cropH := float64(srcH)
	cropW := cropH * target
	if cropW > float64(srcW) {
		cropW = float64(srcW)
		cropH = cropW / target
	}
	g.CropW = evenDown(int(math.Round(cropW)))
	g.CropH = evenDown(int(math.Round(cropH)))
	if g.CropW <= 0 {
		g.CropW = 2
	}
	if g.CropH <= 0 {
		g.CropH = 2
	}
	return g
}

func evenDown(v int) int {
	if v%2 != 0 {
		return v - 1
	}
	return v
}

// Smooth applies an exponential moving average with a deadband so the crop
// does not jitter on every detection.
func Smooth(points []Point, alpha, deadband float64) []Point {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.25
	}
	out := make([]Point, 0, len(points))
	var curX, curY float64
	have := false
	for _, p := range points {
		if !have {
			curX, curY = p.X, p.Y
			have = true
		} else {
			if math.Abs(p.X-curX) > deadband {
				curX += alpha * (p.X - curX)
			}
			if math.Abs(p.Y-curY) > deadband {
				curY += alpha * (p.Y - curY)
			}
		}
		out = append(out, Point{T: p.T, X: curX, Y: curY})
	}
	return out
}

// BuildPath turns raw subject observations into clamped crop offsets.
// Missing observations hold the previous position, so the frame does not snap
// back to centre when the detector briefly loses the face.
func BuildPath(track []Point, g Geometry, alpha, deadband float64) []Point {
	filled := make([]Point, len(track))
	last := Point{X: 0.5, Y: 0.5}
	for i, p := range track {
		if p.X <= 0 && p.Y <= 0 {
			p.X, p.Y = last.X, last.Y
		}
		last = p
		filled[i] = p
	}
	smoothed := Smooth(filled, alpha, deadband)
	for i := range smoothed {
		smoothed[i].X = clamp(smoothed[i].X, 0, 1)
		smoothed[i].Y = clamp(smoothed[i].Y, 0, 1)
	}
	return smoothed
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// OffsetX converts a normalised centre into a crop x offset.
func OffsetX(centerX float64, g Geometry) int {
	x := centerX*float64(g.SrcW) - float64(g.CropW)/2
	maxX := float64(g.SrcW - g.CropW)
	return evenDown(int(math.Round(clamp(x, 0, maxX))))
}

// OffsetY converts a normalised centre into a crop y offset.
func OffsetY(centerY float64, g Geometry) int {
	y := centerY*float64(g.SrcH) - float64(g.CropH)/2
	maxY := float64(g.SrcH - g.CropH)
	return evenDown(int(math.Round(clamp(y, 0, maxY))))
}

// SendCmd renders a sendcmd script that animates the crop filter.
func SendCmd(path []Point, g Geometry) string {
	var b strings.Builder
	prevX, prevY := -1, -1
	for _, p := range path {
		x := OffsetX(p.X, g)
		y := OffsetY(p.Y, g)
		if x == prevX && y == prevY {
			continue
		}
		fmt.Fprintf(&b, "%.3f crop x %d;\n", p.T, x)
		fmt.Fprintf(&b, "%.3f crop y %d;\n", p.T, y)
		prevX, prevY = x, y
	}
	return b.String()
}

// FilterChain builds the ffmpeg video filter for the chosen layout.
func FilterChain(layout string, g Geometry, cmdPath string, outW, outH int) string {
	scale := fmt.Sprintf("scale=%d:%d:flags=lanczos,setsar=1", outW, outH)
	if layout == "center" || cmdPath == "" {
		x := (g.SrcW - g.CropW) / 2
		y := (g.SrcH - g.CropH) / 2
		return fmt.Sprintf("crop=%d:%d:%d:%d,%s", g.CropW, g.CropH, x, y, scale)
	}
	return fmt.Sprintf("sendcmd=f='%s',crop=%d:%d:0:0,%s", escapePath(cmdPath), g.CropW, g.CropH, scale)
}

func escapePath(p string) string {
	p = strings.ReplaceAll(p, `\`, `/`)
	p = strings.ReplaceAll(p, `'`, `\'`)
	p = strings.ReplaceAll(p, `:`, `\:`)
	return p
}
