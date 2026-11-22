// Package config holds the runtime settings for the clipper engine.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config controls capture, highlight detection and export.
type Config struct {
	OutputDir string `json:"output_dir"`

	FFmpegPath  string `json:"ffmpeg_path"`
	FFprobePath string `json:"ffprobe_path"`

	// Detection tuning.
	Threshold     float64 `json:"threshold"`      // normalised loudness/activity score that opens a highlight
	PreRoll       float64 `json:"pre_roll"`       // seconds kept before the detected peak
	PostRoll      float64 `json:"post_roll"`      // seconds kept after the detected peak
	MinClip       float64 `json:"min_clip"`       // clips shorter than this are discarded
	MaxClip       float64 `json:"max_clip"`       // hard cap on clip length
	Cooldown      float64 `json:"cooldown"`       // seconds of silence required between clips
	BufferSeconds float64 `json:"buffer_seconds"` // how much footage the rolling buffer retains
	BaselineSecs  float64 `json:"baseline_secs"`  // loudness baseline window

	// Export.
	OutWidth   int    `json:"out_width"`
	OutHeight  int    `json:"out_height"`
	Layout     string `json:"layout"` // "face" or "center"
	CleanAudio bool   `json:"clean_audio"`
	Captions   bool   `json:"captions"`

	WhisperBin   string `json:"whisper_bin"`
	WhisperModel string `json:"whisper_model"`

	Port int `json:"port"`
}

// Default returns a usable configuration rooted at dir.
func Default(dir string) Config {
	return Config{
		OutputDir:     filepath.Join(dir, "clips"),
		FFmpegPath:    "ffmpeg",
		FFprobePath:   "ffprobe",
		Threshold:     0.55,
		PreRoll:       6,
		PostRoll:      4,
		MinClip:       8,
		MaxClip:       60,
		Cooldown:      8,
		BufferSeconds: 120,
		BaselineSecs:  30,
		OutWidth:      1080,
		OutHeight:     1920,
		Layout:        "face",
		CleanAudio:    true,
		Port:          8765,
	}
}

// Load reads a JSON config, filling gaps with defaults.
func Load(path string) (Config, error) {
	base := Default(filepath.Dir(path))
	data, err := os.ReadFile(path)
	if err != nil {
		return base, err
	}
	if err := json.Unmarshal(data, &base); err != nil {
		return base, err
	}
	return base, nil
}

// Save writes the configuration as JSON.
func (c Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Normalise clamps user supplied values into safe ranges.
func (c *Config) Normalise() {
	if c.Threshold <= 0 || c.Threshold > 1.5 {
		c.Threshold = 0.55
	}
	if c.PreRoll < 0 {
		c.PreRoll = 0
	}
	if c.PostRoll < 0 {
		c.PostRoll = 0
	}
	if c.MinClip <= 0 {
		c.MinClip = 5
	}
	if c.MaxClip < c.MinClip {
		c.MaxClip = c.MinClip
	}
	if c.BufferSeconds < c.MaxClip+c.PreRoll+c.PostRoll+10 {
		c.BufferSeconds = c.MaxClip + c.PreRoll + c.PostRoll + 10
	}
	if c.BaselineSecs <= 0 {
		c.BaselineSecs = 30
	}
	if c.OutWidth <= 0 || c.OutHeight <= 0 {
		c.OutWidth, c.OutHeight = 1080, 1920
	}
	if c.Layout != "center" {
		c.Layout = "face"
	}
	if c.Port <= 0 {
		c.Port = 8765
	}
}
