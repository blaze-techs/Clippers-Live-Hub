package pipeline

import (
	"os"
	"path/filepath"
)

// DeleteClip removes an export and its thumbnail.
func (p *Pipeline) DeleteClip(id string) (bool, error) {
	p.mu.Lock()
	var found *Clip
	rest := make([]Clip, 0, len(p.clips))
	for i := range p.clips {
		if p.clips[i].ID == id {
			c := p.clips[i]
			found = &c
			continue
		}
		rest = append(rest, p.clips[i])
	}
	if found == nil {
		p.mu.Unlock()
		return false, nil
	}
	p.clips = rest
	p.status.ClipCount = len(p.clips)
	snapshot := make([]Clip, len(p.clips))
	copy(snapshot, p.clips)
	p.mu.Unlock()

	if err := os.Remove(filepath.Join(p.cfg.OutputDir, found.File)); err != nil && !os.IsNotExist(err) {
		return true, err
	}
	if found.Thumb != "" {
		os.Remove(filepath.Join(p.cfg.OutputDir, found.Thumb))
	}
	p.saveIndex(snapshot)
	return true, nil
}
