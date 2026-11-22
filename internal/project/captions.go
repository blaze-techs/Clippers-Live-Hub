package project

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// BuildASS renders the caption track as an ASS subtitle file for burning.
func BuildASS(p *Project) string {
	st := p.Style
	var b strings.Builder
	fmt.Fprintf(&b, "[Script Info]\nScriptType: v4.00+\nPlayResX: %d\nPlayResY: %d\n", p.Width, p.Height)
	b.WriteString("WrapStyle: 2\nScaledBorderAndShadow: yes\n\n")
	b.WriteString("[V4+ Styles]\n")
	b.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")

	bold := 0
	if st.Bold {
		bold = -1
	}
	marginV := p.Height * st.MarginV / 100
	if marginV < 0 {
		marginV = 0
	}
	font := st.Font
	if font == "" {
		font = "DejaVu Sans"
	}
	fmt.Fprintf(&b, "Style: Default,%s,%d,%s,&H000000FF,%s,&H64000000,%d,0,0,0,100,100,0,0,1,%d,2,2,60,60,%d,1\n\n",
		font, st.Size, assColor(st.Color), assColor(st.Outline), bold, st.OutlineW, marginV)

	b.WriteString("[Events]\n")
	b.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	for _, c := range p.Cues {
		text := c.Text
		if st.Upper {
			text = strings.ToUpper(text)
		}
		text = strings.ReplaceAll(text, "\n", `\N`)
		text = strings.NewReplacer("{", `\{`, "}", `\}`).Replace(text)
		text = wrapASS(text, 22)
		fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n",
			assTime(c.Start), assTime(c.End), text)
	}
	return b.String()
}

// wrapASS breaks a line into at most two display lines at a word boundary.
func wrapASS(text string, width int) string {
	if strings.Contains(text, `\N`) || len(text) <= width {
		return text
	}
	words := strings.Fields(text)
	var lines []string
	var cur string
	for _, w := range words {
		switch {
		case cur == "":
			cur = w
		case len(cur)+1+len(w) <= width:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) > 2 {
		lines = []string{strings.Join(lines[:len(lines)-1], " "), lines[len(lines)-1]}
	}
	return strings.Join(lines, `\N`)
}

// assColor converts #RRGGBB to ASS &HAABBGGRR.
func assColor(hex string) string {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return "&H00FFFFFF"
	}
	r, err1 := strconv.ParseUint(hex[0:2], 16, 8)
	g, err2 := strconv.ParseUint(hex[2:4], 16, 8)
	bl, err3 := strconv.ParseUint(hex[4:6], 16, 8)
	if err1 != nil || err2 != nil || err3 != nil {
		return "&H00FFFFFF"
	}
	return fmt.Sprintf("&H00%02X%02X%02X", bl, g, r)
}

// assTime formats seconds as H:MM:SS.cc.
func assTime(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	cs := int(sec*100 + 0.5)
	h := cs / 360000
	cs -= h * 360000
	m := cs / 6000
	cs -= m * 6000
	s := cs / 100
	cs -= s * 100
	return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, s, cs)
}

// FormatSRT renders cues as SubRip.
func FormatSRT(cues []Cue) string {
	var b strings.Builder
	for i, c := range cues {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, srtTime(c.Start), srtTime(c.End), c.Text)
	}
	return b.String()
}

func srtTime(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	ms := int(sec*1000 + 0.5)
	h := ms / 3600000
	ms -= h * 3600000
	m := ms / 60000
	ms -= m * 60000
	s := ms / 1000
	ms -= s * 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

var srtIndex = regexp.MustCompile(`(?m)^\d+\s*$`)
var srtArrow = regexp.MustCompile(`(?m)^(\d{1,2}:\d{2}:\d{2}[,.]\d{1,3})\s*-->\s*(\d{1,2}:\d{2}:\d{2}[,.]\d{1,3})`)

// ParseSRT reads SubRip text into cues, tolerating CRLF and missing indices.
func ParseSRT(data string) []Cue {
	norm := strings.ReplaceAll(data, "\r\n", "\n")
	norm = strings.ReplaceAll(norm, "\r", "\n")
	blocks := strings.Split(norm, "\n\n")
	var cues []Cue
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		loc := srtArrow.FindStringSubmatchIndex(block)
		if loc == nil {
			continue
		}
		start, ok1 := parseSRTTime(block[loc[2]:loc[3]])
		end, ok2 := parseSRTTime(block[loc[4]:loc[5]])
		if !ok1 || !ok2 || end <= start {
			continue
		}
		text := strings.TrimSpace(block[loc[1]:])
		text = srtIndex.ReplaceAllString(text, "")
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		cues = append(cues, Cue{ID: NewID(), Start: start, End: end, Text: text})
	}
	return cues
}

func parseSRTTime(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	s = strings.Replace(s, ",", ".", 1)
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}
	return float64(h)*3600 + float64(m)*60 + sec, true
}
