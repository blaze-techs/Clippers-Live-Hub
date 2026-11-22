package analyze

import "math"

// Options tunes detection.
type Options struct {
	Threshold    float64 // normalised score that opens a highlight
	PreRoll      float64
	PostRoll     float64
	MinClip      float64
	MaxClip      float64
	Cooldown     float64
	BaselineSecs float64
	HasAudio     bool // false switches the detector to visual-only scoring
}

type state int

const (
	stateIdle state = iota
	stateActive
	stateCooldown
)

// Detector converts a stream of samples into highlights.
//
// Loudness is normalised against a slow baseline so that a consistently loud
// stream still produces contrast: a moment only counts when it is louder than
// the recent norm. Scene changes add a bounded bonus, which catches visual
// spikes (kills, explosions) that are not loud.
type Detector struct {
	opts Options

	baseline   float64
	baselineN  int
	samples    int
	lastSample float64

	st          state
	peak        float64
	peakT       float64
	activeStart float64
	cooldownEnd float64
}

// NewDetector builds a detector with sane fallbacks.
func NewDetector(opts Options) *Detector {
	if opts.Threshold <= 0 {
		opts.Threshold = 0.55
	}
	if opts.PreRoll < 0 {
		opts.PreRoll = 0
	}
	if opts.MinClip <= 0 {
		opts.MinClip = 5
	}
	if opts.MaxClip < opts.MinClip {
		opts.MaxClip = opts.MinClip
	}
	if opts.BaselineSecs <= 0 {
		opts.BaselineSecs = 30
	}
	return &Detector{opts: opts, st: stateIdle}
}

// Normalise exposes the score for a sample so the UI can show a live meter.
func (d *Detector) Normalise(s Sample) float64 {
	scene := s.Scene
	if scene > 1 {
		scene = 1
	}

	if !d.opts.HasAudio {
		return ClampScore(scene * 1.6)
	}
	if d.baselineN == 0 {
		if s.RMS <= -89 {
			// Audio track present but silent so far: score on visuals.
			return ClampScore(scene * 1.6)
		}
		return 0
	}

	// A 20 dB jump above the running baseline is treated as a full-scale event.
	loud := (s.RMS - d.baseline) / 20.0
	if loud < 0 {
		loud = 0
	}
	if loud > 1.5 {
		loud = 1.5
	}
	// Loudness dominates, visual activity sharpens the peak.
	return ClampScore(loud*0.75 + scene*0.45)
}

// Add feeds one sample and returns any highlight completed by it.
func (d *Detector) Add(s Sample) *Highlight {
	d.lastSample = s.T
	d.samples++

	// Warm the baseline with an exponential average that ignores silence.
	if s.RMS > -70 {
		if d.baselineN == 0 {
			d.baseline = s.RMS
		} else {
			alpha := 2.0 / (d.opts.BaselineSecs + 1)
			d.baseline = (1-alpha)*d.baseline + alpha*s.RMS
		}
		d.baselineN++
	}

	score := d.Normalise(s)

	switch d.st {
	case stateCooldown:
		if s.T >= d.cooldownEnd {
			d.st = stateIdle
		}
		return nil

	case stateIdle:
		if d.samples < 3 {
			return nil // not enough context yet
		}
		if score >= d.opts.Threshold {
			d.st = stateActive
			d.activeStart = s.T
			d.peak = score
			d.peakT = s.T
		}
		return nil

	case stateActive:
		if score > d.peak {
			d.peak = score
			d.peakT = s.T
		}
		// Release when the moment has clearly passed, or when the cap is hit.
		release := score < d.opts.Threshold*0.55 && s.T-d.peakT >= 1.0
		if s.T-d.activeStart >= d.opts.MaxClip {
			release = true
		}
		if !release {
			return nil
		}
		h := d.close(s.T)
		return &h
	}
	return nil
}

func (d *Detector) close(now float64) Highlight {
	start := d.peakT - d.opts.PreRoll
	if start < 0 {
		start = 0
	}
	end := d.peakT + d.opts.PostRoll
	if end < now {
		end = now
	}
	if end-start > d.opts.MaxClip {
		end = start + d.opts.MaxClip
	}
	h := Highlight{
		Start:    start,
		End:      end,
		Peak:     d.peak,
		PeakTime: d.peakT,
		Score:    d.peak,
	}
	d.st = stateCooldown
	d.cooldownEnd = now + d.opts.Cooldown
	d.peak = 0

	if h.End-h.Start < d.opts.MinClip {
		h.End = h.Start + d.opts.MinClip
	}
	return h
}

// Flush closes any open highlight, e.g. when a file ends.
func (d *Detector) Flush() *Highlight {
	if d.st != stateActive {
		return nil
	}
	h := d.close(d.lastSample)
	return &h
}

// Baseline returns the current loudness baseline in dBFS.
func (d *Detector) Baseline() float64 { return d.baseline }

// ClampScore keeps values inside a displayable range.
func ClampScore(v float64) float64 {
	return math.Max(0, math.Min(1.5, v))
}
