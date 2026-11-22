package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blaze-techs/Clippers-Live-Hub/internal/analyze"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/caption"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/compose"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/config"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/ffmpeg"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/media"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/project"
)

// Studio serves the project, timeline, caption and export endpoints.
type Studio struct {
	store *project.Store
	imp   *media.Importer
	tools *ffmpeg.Tools
	cfg   config.Config

	mu      sync.Mutex
	editors map[string]*project.Editor
	jobs    map[string]*renderJob
}

// NewStudio prepares the project store.
func NewStudio(cfg config.Config, tools *ffmpeg.Tools) (*Studio, error) {
	store, err := project.NewStore(filepath.Join(cfg.DataDir, "projects"))
	if err != nil {
		return nil, err
	}
	return &Studio{
		store:   store,
		imp:     &media.Importer{Tools: tools},
		tools:   tools,
		cfg:     cfg,
		editors: map[string]*project.Editor{},
		jobs:    map[string]*renderJob{},
	}, nil
}

// Register mounts the studio routes.
func (s *Studio) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/projects", s.handleProjects)
	mux.HandleFunc("/api/projects/", s.handleProject)
	mux.HandleFunc("/api/system", s.handleSystem)
	mux.HandleFunc("/api/fs", s.handleFS)
}

var videoExts = map[string]bool{
	".mp4": true, ".mov": true, ".mkv": true, ".webm": true, ".avi": true,
	".m4v": true, ".ts": true, ".mts": true, ".flv": true, ".wmv": true,
	".mp3": true, ".m4a": true, ".wav": true, ".aac": true, ".flac": true,
}

// handleFS lists a directory so the desktop UI can pick sources without
// knowing the host's file layout. It only ever reads names and sizes.
func (s *Studio) handleFS(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		if home, err := os.UserHomeDir(); err == nil {
			path = home
		} else {
			path = "."
		}
	}
	path = filepath.Clean(path)
	entries, err := os.ReadDir(path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	type item struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		Size  int64  `json:"size,omitempty"`
		Video bool   `json:"video,omitempty"`
	}
	dirs := []item{}
	files := []item{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(path, name)
		if e.IsDir() {
			dirs = append(dirs, item{Name: name, Path: full})
			continue
		}
		if !videoExts[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		var size int64
		if info, err := e.Info(); err == nil {
			size = info.Size()
		}
		files = append(files, item{Name: name, Path: full, Size: size, Video: true})
	}
	parent := filepath.Dir(path)
	if parent == path {
		parent = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path": path, "parent": parent, "dirs": dirs, "files": files,
	})
}

func (s *Studio) handleSystem(w http.ResponseWriter, r *http.Request) {
	tr := caption.Transcriber{Bin: s.cfg.WhisperBin, Model: s.cfg.WhisperModel}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":       1,
		"data_dir":      s.cfg.DataDir,
		"ffmpeg":        s.tools.FFmpeg,
		"ffprobe":       s.tools.FFprobe,
		"transcribe":    tr.Available(),
		"whisper_bin":   s.cfg.WhisperBin,
		"whisper_model": s.cfg.WhisperModel,
	})
}

func (s *Studio) handleProjects(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.List()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"projects": list})
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.TrimSpace(body.Name) == "" {
			body.Name = "Untitled project"
		}
		p, err := s.store.Create(body.Name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		s.editor(p)
		writeJSON(w, http.StatusCreated, p)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
	}
}

func (s *Studio) handleProject(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/projects/")
	parts := strings.Split(rest, "/")
	id := parts[0]
	if id == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("missing project id"))
		return
	}

	if len(parts) == 1 {
		s.handleDocument(w, r, id)
		return
	}

	switch parts[1] {
	case "ops":
		s.handleOps(w, r, id)
	case "media":
		s.handleImport(w, r, id)
	case "thumb":
		s.handleThumb(w, r, id, parts)
	case "source":
		s.handleSource(w, r, id, parts)
	case "analyze":
		s.handleAnalyze(w, r, id)
	case "transcribe":
		s.handleTranscribe(w, r, id)
	case "cues.srt":
		s.handleSRT(w, r, id)
	case "export":
		s.handleExport(w, r, id, parts)
	case "exports":
		s.handleExports(w, r, id, parts)
	default:
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown endpoint %q", parts[1]))
	}
}

func (s *Studio) handleDocument(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		ed, err := s.load(id)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		s.writeProject(w, ed)
	case http.MethodPut:
		var p project.Project
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		p.ID = id
		p.Validate()
		if err := s.store.Save(&p); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		loaded, err := s.store.Load(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		ed := project.NewEditor(loaded)
		s.mu.Lock()
		s.editors[id] = ed
		s.mu.Unlock()
		s.writeProject(w, ed)
	case http.MethodDelete:
		if err := s.store.Delete(id); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		s.mu.Lock()
		delete(s.editors, id)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
	}
}

func (s *Studio) load(id string) (*project.Editor, error) {
	s.mu.Lock()
	if ed, ok := s.editors[id]; ok {
		s.mu.Unlock()
		return ed, nil
	}
	s.mu.Unlock()

	p, err := s.store.Load(id)
	if err != nil {
		return nil, err
	}
	return s.editor(p), nil
}

func (s *Studio) editor(p *project.Project) *project.Editor {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ed, ok := s.editors[p.ID]; ok {
		return ed
	}
	ed := project.NewEditor(p)
	s.editors[p.ID] = ed
	return ed
}

func (s *Studio) writeProject(w http.ResponseWriter, ed *project.Editor) {
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  ed.P,
		"can_undo": ed.CanUndo(),
		"can_redo": ed.CanRedo(),
	})
}

// opRequest is the discriminated union accepted by /ops.
type opRequest struct {
	Op      string        `json:"op"`
	ClipID  string        `json:"clip_id"`
	TrackID string        `json:"track_id"`
	MediaID string        `json:"media_id"`
	Start   float64       `json:"start"`
	In      float64       `json:"in"`
	Out     float64       `json:"out"`
	Volume  float64       `json:"volume"`
	T       float64       `json:"t"`
	Rect    *project.Rect `json:"rect"`
	CueID   string        `json:"cue_id"`
	Text    string        `json:"text"`
	End     float64       `json:"end"`
	Name    string        `json:"name"`
	Kind    string        `json:"kind"`
	Snap    bool          `json:"snap"`
}

func (s *Studio) handleOps(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}
	ed, err := s.load(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req opRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if req.Snap {
		req.Start = ed.P.Snap(req.Start, 0.15)
	}

	switch req.Op {
	case "add_clip":
		err = ed.AddClip(req.TrackID, project.Clip{
			MediaID: req.MediaID, Start: req.Start, In: req.In, Out: req.Out,
			Crop: req.Rect,
		})
	case "move_clip":
		err = ed.MoveClip(req.ClipID, req.TrackID, req.Start)
	case "trim_clip":
		err = ed.TrimClip(req.ClipID, req.Start, req.In, req.Out)
	case "split_clip":
		_, err = ed.SplitClip(req.ClipID, req.T)
	case "delete_clip":
		err = ed.DeleteClip(req.ClipID)
	case "duplicate_clip":
		_, err = ed.DuplicateClip(req.ClipID)
	case "set_crop":
		err = ed.SetCrop(req.ClipID, req.Rect)
	case "set_pip":
		err = ed.SetPiP(req.ClipID, req.Rect)
	case "set_volume":
		err = ed.SetVolume(req.ClipID, req.Volume)
	case "add_keyframe":
		if req.Rect == nil {
			err = fmt.Errorf("rect required")
		} else {
			err = ed.AddKeyframe(req.ClipID, req.T, *req.Rect)
		}
	case "clear_keyframes":
		err = ed.ClearKeyframes(req.ClipID)
	case "cue_add":
		_, err = ed.AddCue(req.Start, req.End, req.Text)
	case "cue_update":
		err = ed.UpdateCue(req.CueID, req.Start, req.End, req.Text)
	case "cue_delete":
		err = ed.DeleteCue(req.CueID)
	case "add_track":
		err = ed.AddTrack(req.Kind, req.Name)
	case "undo":
		if !ed.Undo() {
			err = fmt.Errorf("nothing to undo")
		}
	case "redo":
		if !ed.Redo() {
			err = fmt.Errorf("nothing to redo")
		}
	default:
		err = fmt.Errorf("unknown op %q", req.Op)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.Save(ed.P); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeProject(w, ed)
}

func (s *Studio) handleImport(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}
	ed, err := s.load(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	path := strings.TrimSpace(body.Path)
	if path == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("path required"))
		return
	}
	path = filepath.Clean(path)

	m, err := s.imp.Import(r.Context(), ed.P, path, s.store.ThumbsDir(id))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ed.P.Media = append(ed.P.Media, *m)
	if err := s.store.Save(ed.P); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Studio) handleThumb(w http.ResponseWriter, r *http.Request, id string, parts []string) {
	if len(parts) < 4 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("thumb needs media id and index"))
		return
	}
	ed, err := s.load(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	m := ed.P.MediaByID(parts[2])
	if m == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("media not found"))
		return
	}
	idx, err := strconv.Atoi(parts[3])
	if err != nil || idx < 0 || idx >= len(m.Thumbs) {
		writeError(w, http.StatusNotFound, fmt.Errorf("thumb not found"))
		return
	}
	http.ServeFile(w, r, filepath.Join(s.store.ThumbsDir(id), m.Thumbs[idx]))
}

func (s *Studio) handleSource(w http.ResponseWriter, r *http.Request, id string, parts []string) {
	if len(parts) < 3 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("media id required"))
		return
	}
	ed, err := s.load(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	m := ed.P.MediaByID(parts[2])
	if m == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("media not found"))
		return
	}
	path := ed.P.Resolve(*m)
	if _, err := os.Stat(path); err != nil {
		writeError(w, http.StatusGone, fmt.Errorf("source file is missing: %s", path))
		return
	}
	http.ServeFile(w, r, path)
}

func (s *Studio) handleAnalyze(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}
	ed, err := s.load(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		MediaID   string  `json:"media_id"`
		Threshold float64 `json:"threshold"`
		PreRoll   float64 `json:"pre_roll"`
		PostRoll  float64 `json:"post_roll"`
		MinClip   float64 `json:"min_clip"`
		MaxClip   float64 `json:"max_clip"`
		Cooldown  float64 `json:"cooldown"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	m := ed.P.MediaByID(body.MediaID)
	if m == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("media not found"))
		return
	}
	path := ed.P.Resolve(*m)
	if _, err := os.Stat(path); err != nil {
		writeError(w, http.StatusGone, fmt.Errorf("source file is missing"))
		return
	}

	samples, err := analyze.New(s.tools).AnalyzeFile(r.Context(), path, m.HasAudio)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	opts := analyze.Options{
		Threshold: body.Threshold, PreRoll: body.PreRoll, PostRoll: body.PostRoll,
		MinClip: body.MinClip, MaxClip: body.MaxClip, Cooldown: body.Cooldown,
		HasAudio: m.HasAudio,
	}
	if opts.PreRoll == 0 {
		opts.PreRoll = 4
	}
	if opts.PostRoll == 0 {
		opts.PostRoll = 2
	}
	det := analyze.NewDetector(opts)
	var out []map[string]any
	add := func(h analyze.Highlight) {
		reason := "loud audio peak"
		if !m.HasAudio {
			reason = "visual activity"
		}
		out = append(out, map[string]any{
			"start": h.Start, "end": h.End, "score": h.Score,
			"peak_time": h.PeakTime, "reason": reason,
		})
	}
	for _, sm := range samples {
		if h := det.Add(sm); h != nil {
			add(*h)
		}
	}
	if h := det.Flush(); h != nil {
		add(*h)
	}
	if out == nil {
		out = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"samples":    len(samples),
		"highlights": out,
	})
}

func (s *Studio) handleTranscribe(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}
	ed, err := s.load(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	tr := caption.Transcriber{Bin: s.cfg.WhisperBin, Model: s.cfg.WhisperModel}
	if !tr.Available() {
		writeError(w, http.StatusNotImplemented,
			fmt.Errorf("transcription needs whisper.cpp: set whisper_bin and whisper_model in settings"))
		return
	}
	var body struct {
		ClipID string `json:"clip_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	_, clip := ed.P.FindClip(body.ClipID)
	if clip == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("clip not found"))
		return
	}
	m := ed.P.MediaByID(clip.MediaID)
	if m == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("media not found"))
		return
	}
	work := filepath.Join(s.store.Dir(id), "cache")
	if err := os.MkdirAll(work, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	srtPath, err := tr.Transcribe(r.Context(), s.tools, ed.P.Resolve(*m), clip.In, clip.Length(), work)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	data, err := os.ReadFile(srtPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	added := 0
	for _, c := range project.ParseSRT(string(data)) {
		if _, err := ed.AddCue(clip.Start+c.Start, clip.Start+c.End, c.Text); err != nil {
			continue
		}
		added++
	}
	if err := s.store.Save(ed.P); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added})
}

func (s *Studio) handleSRT(w http.ResponseWriter, r *http.Request, id string) {
	ed, err := s.load(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/x-subrip; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="captions.srt"`)
		io.WriteString(w, project.FormatSRT(ed.P.Cues))
	case http.MethodPost:
		data, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cues := project.ParseSRT(string(data))
		if len(cues) == 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("no cues found in the uploaded subtitle file"))
			return
		}
		for _, c := range cues {
			if _, err := ed.AddCue(c.Start, c.End, c.Text); err != nil {
				continue
			}
		}
		if err := s.store.Save(ed.P); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		s.writeProject(w, ed)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
	}
}

// renderJob tracks one export.
type renderJob struct {
	ID        string          `json:"id"`
	ProjectID string          `json:"project_id"`
	State     string          `json:"state"` // running, done, failed, cancelled
	Progress  float64         `json:"progress"`
	Error     string          `json:"error,omitempty"`
	Result    *compose.Result `json:"result,omitempty"`
	URL       string          `json:"url,omitempty"`
	Started   time.Time       `json:"started"`
	Finished  *time.Time      `json:"finished,omitempty"`

	cancel context.CancelFunc `json:"-"`
}

func (s *Studio) handleExport(w http.ResponseWriter, r *http.Request, id string, parts []string) {
	if len(parts) >= 3 {
		jobID := parts[2]
		s.mu.Lock()
		job, ok := s.jobs[jobID]
		s.mu.Unlock()
		if !ok || job.ProjectID != id {
			writeError(w, http.StatusNotFound, fmt.Errorf("job not found"))
			return
		}
		if len(parts) >= 4 && parts[3] == "cancel" {
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
				return
			}
			s.mu.Lock()
			if job.cancel != nil && job.State == "running" {
				job.cancel()
			}
			s.mu.Unlock()
		}
		s.mu.Lock()
		snapshot := *job
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, snapshot)
		return
	}

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}
	ed, err := s.load(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err := s.store.Save(ed.P); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	exports := s.store.ExportsDir(id)
	if err := os.MkdirAll(exports, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	name := fmt.Sprintf("clip_%s.mp4", time.Now().Format("20060102_150405"))
	out := filepath.Join(exports, name)

	ctx, cancel := context.WithCancel(context.Background())
	job := &renderJob{
		ID: project.NewID(), ProjectID: id, State: "running",
		Started: time.Now(), cancel: cancel,
	}
	s.mu.Lock()
	s.jobs[job.ID] = job
	s.mu.Unlock()

	go func() {
		res, err := compose.Run(ctx, compose.Options{
			Tools: s.tools, Project: ed.P, Out: out,
			WorkDir: filepath.Join(s.store.Dir(id), "cache"),
			Progress: func(f float64) {
				s.mu.Lock()
				job.Progress = f
				s.mu.Unlock()
			},
		})
		now := time.Now()
		s.mu.Lock()
		defer s.mu.Unlock()
		job.Finished = &now
		switch {
		case err == context.Canceled:
			job.State = "cancelled"
			job.Error = "cancelled"
			_ = os.Remove(out)
		case err != nil:
			job.State = "failed"
			job.Error = err.Error()
			_ = os.Remove(out)
		default:
			job.State = "done"
			job.Progress = 1
			job.Result = res
			job.URL = "/api/projects/" + id + "/exports/" + name
		}
	}()

	writeJSON(w, http.StatusAccepted, job)
}

func (s *Studio) handleExports(w http.ResponseWriter, r *http.Request, id string, parts []string) {
	if len(parts) >= 3 {
		name := parts[2]
		if strings.ContainsAny(name, `/\`) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid name"))
			return
		}
		http.ServeFile(w, r, filepath.Join(s.store.ExportsDir(id), name))
		return
	}
	list, err := s.store.Exports(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"exports": list})
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}
