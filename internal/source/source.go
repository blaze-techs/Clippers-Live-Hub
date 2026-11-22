// Package source turns a capture target into ffmpeg input arguments.
package source

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Kind identifies where footage comes from.
type Kind string

const (
	Screen Kind = "screen"
	Window Kind = "window"
	URL    Kind = "url"
	File   Kind = "file"
)

// Source describes one capture target.
type Source struct {
	Kind        Kind    `json:"kind"`
	Path        string  `json:"path"`         // file path or stream URL
	WindowTitle string  `json:"window_title"` // window capture only
	Display     string  `json:"display"`      // X11 display / avfoundation device
	AudioDevice string  `json:"audio_device"` // DirectShow / avfoundation audio device
	FPS         int     `json:"fps"`
	Realtime    bool    `json:"realtime"` // pace file playback at real speed
	NoAudio     bool    `json:"no_audio"`
	StartAt     float64 `json:"start_at"`
}

// IsLive reports whether the source is an unbounded live feed.
func (s Source) IsLive() bool { return s.Kind != File }

// Validate checks the source is usable on this machine.
func (s Source) Validate() error {
	switch s.Kind {
	case File:
		if s.Path == "" {
			return fmt.Errorf("file source requires a path")
		}
		if _, err := os.Stat(s.Path); err != nil {
			return fmt.Errorf("file not found: %w", err)
		}
	case URL:
		if !strings.HasPrefix(s.Path, "http") && !strings.HasPrefix(s.Path, "rtmp") &&
			!strings.HasPrefix(s.Path, "rtsp") && !strings.HasPrefix(s.Path, "srt") &&
			!strings.HasPrefix(s.Path, "udp") {
			return fmt.Errorf("unsupported stream URL: %s", s.Path)
		}
	case Window:
		if runtime.GOOS != "windows" {
			return fmt.Errorf("window capture is only supported on Windows")
		}
		if s.WindowTitle == "" {
			return fmt.Errorf("window capture requires a window title")
		}
	case Screen:
	default:
		return fmt.Errorf("unknown source kind %q", s.Kind)
	}
	return nil
}

// InputArgs returns the full set of ffmpeg input arguments (including -i).
func (s Source) InputArgs() []string {
	fps := s.FPS
	if fps <= 0 {
		fps = 30
	}
	rate := strconv.Itoa(fps)
	var args []string

	switch s.Kind {
	case File:
		if s.Realtime {
			args = append(args, "-re")
		}
		if s.StartAt > 0 {
			args = append(args, "-ss", strconv.FormatFloat(s.StartAt, 'f', 3, 64))
		}
		args = append(args, "-i", s.Path)
		return args

	case URL:
		if strings.HasPrefix(s.Path, "http") {
			args = append(args,
				"-reconnect", "1",
				"-reconnect_streamed", "1",
				"-reconnect_delay_max", "5",
			)
		}
		args = append(args, "-i", s.Path)
		return args

	case Window:
		args = append(args, "-f", "gdigrab", "-framerate", rate, "-i", "title="+s.WindowTitle)

	case Screen:
		switch runtime.GOOS {
		case "windows":
			args = append(args, "-f", "gdigrab", "-framerate", rate, "-i", "desktop")
		case "darwin":
			disp := s.Display
			if disp == "" {
				disp = "1"
			}
			args = append(args, "-f", "avfoundation", "-framerate", rate, "-i", disp+":none")
		default:
			disp := s.Display
			if disp == "" {
				disp = ":0.0"
			}
			args = append(args, "-f", "x11grab", "-framerate", rate, "-i", disp)
		}
	}

	if !s.NoAudio && s.AudioDevice != "" {
		switch runtime.GOOS {
		case "windows":
			args = append(args, "-f", "dshow", "-i", "audio="+s.AudioDevice)
		case "darwin":
			args = append(args, "-f", "avfoundation", "-i", "none:"+s.AudioDevice)
		}
	}
	return args
}

// HasAudioInput reports whether a separate audio device is attached.
func (s Source) HasAudioInput() bool {
	if s.Kind == File || s.Kind == URL {
		return !s.NoAudio
	}
	return !s.NoAudio && s.AudioDevice != ""
}

// Label is a short human readable description.
func (s Source) Label() string {
	switch s.Kind {
	case File:
		return "file: " + s.Path
	case URL:
		return "stream: " + s.Path
	case Window:
		return "window: " + s.WindowTitle
	default:
		return "screen capture"
	}
}
