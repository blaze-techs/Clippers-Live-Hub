package compose

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/blaze-techs/Clippers-Live-Hub/internal/ffmpeg"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/project"
)

func tools(t *testing.T) *ffmpeg.Tools {
	t.Helper()
	tl, err := ffmpeg.Locate("", "")
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	return tl
}

// makeVideo writes a solid-colour clip with a sine tone so audio and video
// paths are both exercised.
func makeVideo(t *testing.T, dir, name, colour string, w, h int, dur float64) string {
	t.Helper()
	out := filepath.Join(dir, name)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=%s:s=%dx%d:r=30:d=%s", colour, w, h, f(dur)),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:duration=%s", f(dur)),
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", out,
	}
	cmd := exec.Command(tools(t).FFmpeg, args...)
	if outb, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make fixture: %v %s", err, outb)
	}
	return out
}

// makeSplitVideo is red on the left half and blue on the right half, so a crop
// is visible as a colour shift in the output.
func makeSplitVideo(t *testing.T, dir string, w, h int, dur float64) string {
	t.Helper()
	out := filepath.Join(dir, "split.mp4")
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=red:s=%dx%d:r=30:d=%s", w, h, f(dur)),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:duration=%s", f(dur)),
		"-vf", fmt.Sprintf("drawbox=x=%d:y=0:w=%d:h=%d:color=blue:t=fill", w/2, w/2, h),
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", out,
	}
	cmd := exec.Command(tools(t).FFmpeg, args...)
	if outb, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make split fixture: %v %s", err, outb)
	}
	return out
}

func f(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

func newProject(t *testing.T, w, h int, dur float64) *project.Project {
	t.Helper()
	p := project.New("test")
	p.Width, p.Height = w, h
	p.Export.Width, p.Export.Height = w, h
	p.Export.Preset = "ultrafast"
	p.Export.CRF = 28
	p.Export.Clean = false
	_ = dur
	return p
}

func addMedia(t *testing.T, p *project.Project, path string) string {
	t.Helper()
	info, err := tools(t).Probe(context.Background(), path)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	m := project.Media{
		ID: project.NewID(), Name: filepath.Base(path), Path: path,
		Duration: info.Duration, Width: info.Width, Height: info.Height,
		FPS: info.FPS, HasVideo: info.HasVideo, HasAudio: info.HasAudio,
	}
	p.Media = append(p.Media, m)
	return m.ID
}

func render(t *testing.T, p *project.Project, dir, name string) *Result {
	t.Helper()
	out := filepath.Join(dir, name)
	res, err := Run(context.Background(), Options{
		Tools: tools(t), Project: p, Out: out, WorkDir: dir,
		OnLog: func(s string) { t.Log(s) },
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return res
}

func frame(t *testing.T, path string, at float64) image.Image {
	t.Helper()
	cmd := exec.Command(tools(t).FFmpeg, "-hide_banner", "-loglevel", "error",
		"-ss", f(at), "-i", path, "-frames:v", "1", "-f", "image2pipe", "-vcodec", "png", "-")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("frame: %v %s", err, errb.String())
	}
	img, err := png.Decode(&out)
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	return img
}

// dominant reports which of red/blue covers more of the frame.
func dominant(t *testing.T, img image.Image) string {
	t.Helper()
	b := img.Bounds()
	red, blue := 0, 0
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			r, g, bl, _ := img.At(x, y).RGBA()
			R, G, B := int(r>>8), int(g>>8), int(bl>>8)
			switch {
			case R > 120 && B < 90 && G < 90:
				red++
			case B > 120 && R < 90 && G < 90:
				blue++
			}
		}
	}
	if red > blue {
		return "red"
	}
	return "blue"
}

func TestCropChangesExportedFraming(t *testing.T) {
	dir := t.TempDir()
	src := makeSplitVideo(t, dir, 640, 360, 3)

	p := newProject(t, 540, 960, 3)
	mediaID := addMedia(t, p, src)
	ed := project.NewEditor(p)
	track := p.FirstVideoTrack().ID
	if err := ed.AddClip(track, project.Clip{MediaID: mediaID, Start: 0, In: 0, Out: 3,
		Crop: &project.Rect{X: 0, Y: 0, W: 0.5, H: 1}}); err != nil {
		t.Fatal(err)
	}

	res := render(t, p, dir, "left.mp4")
	if res.Width != 540 || res.Height != 960 {
		t.Fatalf("expected 540x960, got %dx%d", res.Width, res.Height)
	}
	if res.Duration < 2.5 || res.Duration > 3.5 {
		t.Fatalf("expected ~3s, got %.2f", res.Duration)
	}
	if got := dominant(t, frame(t, res.Path, 1.0)); got != "red" {
		t.Errorf("crop of the left half should keep red, got %s", got)
	}

	if err := ed.SetCrop(p.FirstVideoTrack().Clips[0].ID, &project.Rect{X: 0.5, Y: 0, W: 0.5, H: 1}); err != nil {
		t.Fatal(err)
	}
	res = render(t, p, dir, "right.mp4")
	if got := dominant(t, frame(t, res.Path, 1.0)); got != "blue" {
		t.Errorf("crop of the right half should keep blue, got %s", got)
	}
}

func TestPictureInPictureAndCaptions(t *testing.T) {
	dir := t.TempDir()
	base := makeVideo(t, dir, "base.mp4", "red", 640, 360, 4)
	over := makeVideo(t, dir, "over.mp4", "green", 320, 180, 4)

	p := newProject(t, 540, 960, 4)
	baseID := addMedia(t, p, base)
	overID := addMedia(t, p, over)

	ed := project.NewEditor(p)
	videoTrack := p.FirstVideoTrack().ID
	overlayTrack := ""
	for _, tr := range p.Tracks {
		if tr.Kind == project.TrackOverlay {
			overlayTrack = tr.ID
		}
	}
	if overlayTrack == "" {
		t.Fatal("no overlay track")
	}
	if err := ed.AddClip(videoTrack, project.Clip{MediaID: baseID, Start: 0, In: 0, Out: 4}); err != nil {
		t.Fatal(err)
	}
	if err := ed.AddClip(overlayTrack, project.Clip{
		MediaID: overID, Start: 0, In: 0, Out: 4,
		PiP: &project.Rect{X: 0.55, Y: 0.05, W: 0.4, H: 0.3},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ed.AddCue(0.5, 3.5, "HELLO WORLD"); err != nil {
		t.Fatal(err)
	}

	p.Export.Burn = false
	plain := render(t, p, dir, "plain.mp4")

	p.Export.Burn = true
	burned := render(t, p, dir, "burned.mp4")

	img := frame(t, burned.Path, 2.0)
	b := img.Bounds()
	cx, cy := b.Max.X*3/4, b.Max.Y/5
	r, g, bl, _ := img.At(cx, cy).RGBA()
	if !(int(g>>8) > 90 && int(r>>8) < 90 && int(bl>>8) < 90) {
		t.Errorf("picture-in-picture region should be green, got r=%d g=%d b=%d",
			r>>8, g>>8, bl>>8)
	}

	if diff := lowerThirdDiff(t, frame(t, plain.Path, 2.0), img); diff < 3 {
		t.Errorf("burning captions should change the lower third, mean diff %.2f", diff)
	}
}

// lowerThirdDiff is the mean per-channel difference over the bottom quarter.
func lowerThirdDiff(t *testing.T, a, b image.Image) float64 {
	t.Helper()
	bd := a.Bounds()
	start := bd.Max.Y * 3 / 4
	var sum float64
	var n int
	for y := start; y < bd.Max.Y; y += 2 {
		for x := bd.Min.X; x < bd.Max.X; x += 2 {
			r1, g1, b1, _ := a.At(x, y).RGBA()
			r2, g2, b2, _ := b.At(x, y).RGBA()
			sum += float64(absInt(int(r1>>8)-int(r2>>8)) +
				absInt(int(g1>>8)-int(g2>>8)) +
				absInt(int(b1>>8)-int(b2>>8)))
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n) / 3
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func TestEmptyTimelineIsRejected(t *testing.T) {
	p := project.New("empty")
	_, err := Run(context.Background(), Options{
		Tools: tools(t), Project: p, Out: filepath.Join(t.TempDir(), "x.mp4"),
	})
	if err != ErrEmpty {
		t.Fatalf("expected ErrEmpty, got %v", err)
	}
	_ = os.Remove("")
}
