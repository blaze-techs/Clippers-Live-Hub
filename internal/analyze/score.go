// Package analyze detects highlight moments from audio loudness and video activity.
package analyze

// Sample is one observation of the incoming stream.
type Sample struct {
	T     float64 // media timestamp in seconds
	RMS   float64 // audio loudness in dBFS (0 when silent or absent)
	Scene float64 // scene change score 0..1
}

// Highlight is a detected moment worth clipping.
type Highlight struct {
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
	Peak     float64 `json:"peak"`
	PeakTime float64 `json:"peak_time"`
	Score    float64 `json:"score"`
}
