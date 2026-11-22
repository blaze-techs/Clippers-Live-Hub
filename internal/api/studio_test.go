package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/blaze-techs/Clippers-Live-Hub/internal/config"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/ffmpeg"
)

type harness struct {
	t   *testing.T
	srv *httptest.Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	tools, err := ffmpeg.Locate("", "")
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	cfg := config.Default(t.TempDir())
	cfg.Normalise()
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := NewStudio(cfg, tools)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	st.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &harness{t: t, srv: srv}
}

func (h *harness) do(method, path string, body any, out any) int {
	h.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if s, ok := body.(string); ok {
			buf.WriteString(s)
		} else if err := json.NewEncoder(&buf).Encode(body); err != nil {
			h.t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, h.srv.URL+path, &buf)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil && res.StatusCode < 300 {
			h.t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return res.StatusCode
}

func (h *harness) get(path string, out any) int { return h.do(http.MethodGet, path, nil, out) }

func fixture(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "src.mp4")
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=teal:s=320x180:r=25:d=3",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", out,
	}
	cmd := exec.Command("ffmpeg", args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, b)
	}
	return out
}

type docResponse struct {
	Project map[string]any `json:"project"`
	CanUndo bool           `json:"can_undo"`
	CanRedo bool           `json:"can_redo"`
}

func TestStudioWorkflow(t *testing.T) {
	h := newHarness(t)
	src := fixture(t, t.TempDir())

	var created map[string]any
	if code := h.do(http.MethodPost, "/api/projects", map[string]any{"name": "Integration"}, &created); code != http.StatusCreated {
		t.Fatalf("create project: %d", code)
	}
	id := created["id"].(string)

	var media map[string]any
	if code := h.do(http.MethodPost, "/api/projects/"+id+"/media", map[string]any{"path": src}, &media); code != http.StatusCreated {
		t.Fatalf("import: %d", code)
	}
	if media["duration"].(float64) < 2.5 {
		t.Fatalf("probe failed: %+v", media)
	}
	if n := len(media["peaks"].([]any)); n != 300 {
		t.Errorf("expected 300 waveform peaks, got %d", n)
	}
	mediaID := media["id"].(string)

	var doc docResponse
	h.get("/api/projects/"+id, &doc)
	var trackID string
	for _, tr := range doc.Project["tracks"].([]any) {
		track := tr.(map[string]any)
		if track["kind"] == "video" {
			trackID = track["id"].(string)
		}
	}

	if code := h.do(http.MethodPost, "/api/projects/"+id+"/ops", map[string]any{
		"op": "add_clip", "track_id": trackID, "media_id": mediaID,
		"start": 0, "in": 0, "out": 3,
	}, &doc); code != http.StatusOK {
		t.Fatalf("add_clip: %d", code)
	}
	if !doc.CanUndo {
		t.Error("adding a clip should be undoable")
	}

	if code := h.do(http.MethodPost, "/api/projects/"+id+"/ops", map[string]any{
		"op": "cue_add", "start": 0.2, "end": 2.0, "text": "integration caption",
	}, &doc); code != http.StatusOK {
		t.Fatalf("cue_add: %d", code)
	}

	// A bad op must be rejected without corrupting the document.
	if code := h.do(http.MethodPost, "/api/projects/"+id+"/ops", map[string]any{"op": "nope"}, nil); code != http.StatusBadRequest {
		t.Errorf("unknown op: expected 400, got %d", code)
	}

	var job map[string]any
	if code := h.do(http.MethodPost, "/api/projects/"+id+"/export", map[string]any{}, &job); code != http.StatusAccepted {
		t.Fatalf("export start: %d", code)
	}
	jobID := job["id"].(string)

	deadline := time.Now().Add(120 * time.Second)
	var state string
	var progress float64
	for time.Now().Before(deadline) {
		var cur map[string]any
		h.get("/api/projects/"+id+"/export/"+jobID, &cur)
		state, _ = cur["state"].(string)
		if p, ok := cur["progress"].(float64); ok {
			progress = p
		}
		if state != "running" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if state != "done" {
		t.Fatalf("render did not finish: state=%q", state)
	}
	if progress < 1 {
		t.Errorf("progress should reach 1, got %v", progress)
	}

	var exports map[string]any
	if code := h.get("/api/projects/"+id+"/exports", &exports); code != http.StatusOK {
		t.Fatalf("list exports: %d", code)
	}
	list := exports["exports"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected 1 export, got %d", len(list))
	}
	first := list[0].(map[string]any)
	url := first["url"].(string)

	res, err := http.Get(h.srv.URL + url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("download export: %d", res.StatusCode)
	}
	if res.ContentLength < 1000 {
		t.Errorf("export suspiciously small: %d bytes", res.ContentLength)
	}

	var missing map[string]any
	if code := h.get("/api/projects/does-not-exist", &missing); code != http.StatusNotFound {
		t.Errorf("missing project: expected 404, got %d", code)
	}
}

func TestImportRejectsMissingFile(t *testing.T) {
	h := newHarness(t)
	var created map[string]any
	h.do(http.MethodPost, "/api/projects", map[string]any{"name": "x"}, &created)
	id := created["id"].(string)

	var out map[string]any
	code := h.do(http.MethodPost, "/api/projects/"+id+"/media",
		map[string]any{"path": filepath.Join(t.TempDir(), "nope.mp4")}, &out)
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a missing file, got %d", code)
	}
	if out["error"] == nil {
		t.Error("expected an error message")
	}
}

func TestSRTImportAndExport(t *testing.T) {
	h := newHarness(t)
	var created map[string]any
	h.do(http.MethodPost, "/api/projects", map[string]any{"name": "srt"}, &created)
	id := created["id"].(string)

	srt := "1\n00:00:01,000 --> 00:00:02,500\nhello there\n"
	var doc docResponse
	if code := h.do(http.MethodPost, "/api/projects/"+id+"/cues.srt", srt, &doc); code != http.StatusOK {
		t.Fatalf("import srt: %d", code)
	}
	cues := doc.Project["cues"].([]any)
	if len(cues) != 1 {
		t.Fatalf("expected 1 cue, got %d", len(cues))
	}

	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/api/projects/"+id+"/cues.srt", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(res.Body)
	if !bytes.Contains(buf.Bytes(), []byte("00:00:01,000 --> 00:00:02,500")) {
		t.Errorf("exported SRT wrong:\n%s", buf.String())
	}
}

func TestFileBrowserHidesNonMedia(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "clip.mp4"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)

	var out map[string]any
	if code := h.get("/api/fs?path="+dir, &out); code != http.StatusOK {
		t.Fatalf("fs: %d", code)
	}
	files := out["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("expected only the video, got %d entries", len(files))
	}
	if files[0].(map[string]any)["name"] != "clip.mp4" {
		t.Errorf("wrong file listed: %v", files[0])
	}
}
