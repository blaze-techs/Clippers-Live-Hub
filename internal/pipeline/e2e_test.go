package pipeline_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/blaze-techs/Clippers-Live-Hub/internal/config"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/ffmpeg"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/pipeline"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/source"
)

func requireFFmpeg(t *testing.T) *ffmpeg.Tools {
	t.Helper()
	tools, err := ffmpeg.Locate("", "")
	if err != nil {
		t.Skip("ffmpeg not installed:", err)
	}
	return tools
}

// makeFixture renders a quiet clip with two loud bursts so detection has
// something unambiguous to find.
func makeFixture(t *testing.T, tools *ffmpeg.Tools, path string, dur float64, bursts [][2]float64) {
	t.Helper()
	var enable string
	for i, b := range bursts {
		if i > 0 {
			enable += "+"
		}
		enable += "between(t," + strconv.FormatFloat(b[0], 'f', 1, 64) + "," + strconv.FormatFloat(b[1], 'f', 1, 64) + ")"
	}
	d := strconv.FormatFloat(dur, 'f', 1, 64)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=0x102030:s=640x360:r=30:d=" + d + ",drawbox=x='mod(t*80\\,640)':y=100:w=120:h=120:color=0xff4d3d:t=fill",
		"-f", "lavfi", "-i", "sine=frequency=300:duration=" + d + ":sample_rate=48000",
		"-filter_complex", "[1:a]volume=0.02[a0];[a0]volume=25:enable='" + enable + "'[a]",
		"-map", "0:v", "-map", "[a]",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", path,
	}
	cmd := exec.Command(tools.FFmpeg, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v: %s", err, out)
	}
}

func waitDone(t *testing.T, p *pipeline.Pipeline, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if !p.Status().Running {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	p.Stop()
	t.Fatalf("session did not finish within %s", limit)
}

func assertVerticalClip(t *testing.T, tools *ffmpeg.Tools, path string) {
	t.Helper()
	info, err := tools.Probe(context.Background(), path)
	if err != nil {
		t.Fatalf("probe %s: %v", path, err)
	}
	if info.Width != 1080 || info.Height != 1920 {
		t.Errorf("%s is %dx%d, want 1080x1920", filepath.Base(path), info.Width, info.Height)
	}
	if !info.HasAudio {
		t.Errorf("%s has no audio track", filepath.Base(path))
	}
	if info.Duration < 4 {
		t.Errorf("%s is only %.1fs long", filepath.Base(path), info.Duration)
	}
}

func baseConfig(dir string) config.Config {
	cfg := config.Default(dir)
	cfg.OutputDir = filepath.Join(dir, "clips")
	cfg.Threshold = 0.55
	cfg.PreRoll = 6
	cfg.PostRoll = 4
	cfg.MinClip = 6
	cfg.MaxClip = 20
	cfg.Cooldown = 8
	cfg.BufferSeconds = 60
	cfg.BaselineSecs = 30
	cfg.Layout = "face"
	cfg.CleanAudio = true
	cfg.Captions = false
	return cfg
}

func TestFileModeProducesVerticalClips(t *testing.T) {
	tools := requireFFmpeg(t)
	dir := t.TempDir()
	fixture := filepath.Join(dir, "match.mp4")
	makeFixture(t, tools, fixture, 60, [][2]float64{{20, 23}, {40, 43}})

	cfg := baseConfig(dir)
	p := pipeline.New(cfg, tools)
	if err := p.Start(source.Source{Kind: source.File, Path: fixture}); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone(t, p, 5*time.Minute)

	clips := p.Clips()
	if len(clips) != 2 {
		t.Fatalf("expected 2 clips, got %d (status: %+v)", len(clips), p.Status())
	}
	for _, c := range clips {
		path := filepath.Join(cfg.OutputDir, c.File)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("clip missing: %v", err)
		}
		assertVerticalClip(t, tools, path)
	}
	// The two bursts are far apart, so the clips should not overlap.
	if !(clips[0].Start > clips[1].End || clips[1].Start > clips[0].End) {
		t.Errorf("clips overlap: %+v", clips)
	}
}

func TestLiveModeClipsFromRollingBuffer(t *testing.T) {
	tools := requireFFmpeg(t)
	dir := t.TempDir()
	fixture := filepath.Join(dir, "stream.mp4")
	makeFixture(t, tools, fixture, 14, [][2]float64{{6, 8}})

	cfg := baseConfig(dir)
	cfg.PreRoll = 3
	cfg.PostRoll = 2
	cfg.MinClip = 4
	cfg.MaxClip = 10
	cfg.Cooldown = 4
	cfg.BufferSeconds = 30

	p := pipeline.New(cfg, tools)
	// Realtime paces the file at wall-clock speed so the segmenter behaves
	// like a live capture.
	src := source.Source{Kind: source.File, Path: fixture, Realtime: true}
	if err := p.Start(src); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone(t, p, 3*time.Minute)

	clips := p.Clips()
	if len(clips) == 0 {
		t.Fatalf("live mode produced no clips (status: %+v)", p.Status())
	}
	assertVerticalClip(t, tools, filepath.Join(cfg.OutputDir, clips[0].File))
}
