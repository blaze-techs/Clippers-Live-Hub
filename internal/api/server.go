// Package api exposes the local control surface used by the desktop UI.
package api

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blaze-techs/Clippers-Live-Hub/internal/config"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/ffmpeg"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/pipeline"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/source"
)

// Server wires HTTP handlers to the pipeline.
type Server struct {
	pipe   *pipeline.Pipeline
	tools  *ffmpeg.Tools
	assets fs.FS

	mu  sync.RWMutex
	cfg config.Config
}

// New builds a server.
func New(cfg config.Config, pipe *pipeline.Pipeline, tools *ffmpeg.Tools, assets fs.FS) *Server {
	return &Server{pipe: pipe, tools: tools, assets: assets, cfg: cfg}
}

// Handler returns the root mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/start", s.handleStart)
	mux.HandleFunc("/api/stop", s.handleStop)
	mux.HandleFunc("/api/clips", s.handleClips)
	mux.HandleFunc("/api/clips/", s.handleClipByID)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/environment", s.handleEnvironment)
	mux.HandleFunc("/clips/", s.handleClipFile)
	return logRequests(mux)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(s.assets, "index.html")
	if err != nil {
		http.Error(w, "ui missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.pipe.Status())
}

func (s *Server) handleClips(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.pipe.Clips())
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var src source.Source
	if err := json.NewDecoder(r.Body).Decode(&src); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid source: " + err.Error()})
		return
	}
	if err := s.pipe.Start(src); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "started"})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	s.pipe.Stop()
	writeJSON(w, http.StatusOK, map[string]string{"ok": "stopped"})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	id, ch := s.pipe.Subscribe()
	defer s.pipe.Unsubscribe(id)

	send := func(ev pipeline.Event) {
		data, err := json.Marshal(ev)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	send(pipeline.Event{Type: "status", Status: ptr(s.pipe.Status())})

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			send(ev)
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func ptr[T any](v T) *T { return &v }

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.RLock()
		defer s.mu.RUnlock()
		writeJSON(w, http.StatusOK, s.cfg)
	case http.MethodPost:
		if s.pipe.Status().Running {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "stop the session before changing settings"})
			return
		}
		var incoming config.Config
		if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		incoming.Normalise()
		s.mu.Lock()
		s.cfg = incoming
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, incoming)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleEnvironment(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
		"ffmpeg":     s.tools.FFmpeg,
		"output":     s.cfg.OutputDir,
		"screen":     runtime.GOOS == "windows" || runtime.GOOS == "linux" || runtime.GOOS == "darwin",
		"window":     runtime.GOOS == "windows",
		"whisper":    s.cfg.WhisperBin != "",
		"clip_count": len(s.pipe.Clips()),
	})
}

func (s *Server) handleClipFile(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/clips/")
	name = filepath.Base(name)
	if name == "" || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.cfg.OutputDir, name)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(name, ".mp4") {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Accept-Ranges", "bytes")
	}
	http.ServeFile(w, r, path)
}

func (s *Server) handleClipByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/clips/")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	ok, err := s.pipe.DeleteClip(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "deleted"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// ParsePort validates a port string.
func ParsePort(s string, fallback int) int {
	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 || v > 65535 {
		return fallback
	}
	return v
}
