package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Store keeps projects on disk, one directory per project.
//
// Layout:
//
//	<root>/<id>/project.json
//	<root>/<id>/project.json.tmp   (in-flight autosave)
//	<root>/<id>/thumbs/
//	<root>/<id>/exports/
type Store struct {
	Root string
}

// NewStore prepares the project root.
func NewStore(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Store{Root: root}, nil
}

// Dir is the on-disk directory for a project.
func (s *Store) Dir(id string) string { return filepath.Join(s.Root, id) }

// ThumbsDir is where preview frames live.
func (s *Store) ThumbsDir(id string) string { return filepath.Join(s.Dir(id), "thumbs") }

// ExportsDir is where finished renders live.
func (s *Store) ExportsDir(id string) string { return filepath.Join(s.Dir(id), "exports") }

// Create writes a new project.
func (s *Store) Create(name string) (*Project, error) {
	p := New(name)
	if err := os.MkdirAll(s.ThumbsDir(p.ID), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.ExportsDir(p.ID), 0o755); err != nil {
		return nil, err
	}
	p.Dir = s.Dir(p.ID)
	if err := s.Save(p); err != nil {
		return nil, err
	}
	return p, nil
}

// Load reads a project, recovering an interrupted autosave when the main
// document is missing or unreadable.
func (s *Store) Load(id string) (*Project, error) {
	if id == "" || strings.ContainsAny(id, `/\`) {
		return nil, fmt.Errorf("invalid project id")
	}
	dir := s.Dir(id)
	path := filepath.Join(dir, "project.json")

	p, err := readProject(path)
	if err != nil {
		tmp := filepath.Join(dir, "project.json.tmp")
		if rec, rerr := readProject(tmp); rerr == nil {
			p, err = rec, nil
			_ = os.Rename(tmp, path)
		}
	}
	if err != nil {
		return nil, err
	}
	p.Dir = dir
	return p, nil
}

func readProject(path string) (*Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p, err := Unmarshal(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return p, nil
}

// Save writes the project atomically: a temp file is fsynced and renamed so a
// crash mid-write leaves the previous document intact.
func (s *Store) Save(p *Project) error {
	if p == nil {
		return fmt.Errorf("nil project")
	}
	dir := s.Dir(p.ID)
	if p.Dir != "" {
		dir = p.Dir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p.Touch()
	data, err := p.Marshal()
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "project.json.tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "project.json"))
}

// Delete removes a project and its assets.
func (s *Store) Delete(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("invalid project id")
	}
	return os.RemoveAll(s.Dir(id))
}

// Summary is the list view of a project.
type Summary struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Updated  time.Time `json:"updated_at"`
	Duration float64   `json:"duration"`
	Clips    int       `json:"clips"`
	Media    int       `json:"media"`
	Thumb    string    `json:"thumb,omitempty"`
}

// List returns every readable project, newest first.
func (s *Store) List() ([]Summary, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return nil, err
	}
	out := []Summary{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := s.Load(e.Name())
		if err != nil {
			continue
		}
		sum := Summary{
			ID: p.ID, Name: p.Name, Updated: p.UpdatedAt,
			Duration: p.Duration(), Media: len(p.Media),
		}
		for _, t := range p.Tracks {
			sum.Clips += len(t.Clips)
		}
		if p.Media != nil {
			for _, m := range p.Media {
				if len(m.Thumbs) > 0 {
					sum.Thumb = "/api/projects/" + p.ID + "/thumb/" + m.ID + "/0"
					break
				}
			}
		}
		out = append(out, sum)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// Exports lists finished renders, newest first.
func (s *Store) Exports(id string) ([]ExportFile, error) {
	dir := s.ExportsDir(id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := []ExportFile{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".mp4") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, ExportFile{
			Name: e.Name(),
			URL:  "/api/projects/" + id + "/exports/" + e.Name(),
			Size: info.Size(),
			At:   info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// ExportFile is one finished render.
type ExportFile struct {
	Name string    `json:"name"`
	URL  string    `json:"url"`
	Size int64     `json:"size"`
	At   time.Time `json:"at"`
}

// WriteJSON is a small helper for sidecar files such as the caption export.
func (s *Store) WriteJSON(id, name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := s.Dir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), data, 0o644)
}
