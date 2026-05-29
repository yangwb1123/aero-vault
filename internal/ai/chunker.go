package ai

import "strings"

// Chunker splits text into overlapping windows. Window/overlap are in runes,
// so the chunker is safe on UTF-8 / CJK without mid-codepoint breaks.
type Chunker struct {
	Window  int
	Overlap int
}

func NewChunker() *Chunker { return &Chunker{Window: 600, Overlap: 80} }

func (c *Chunker) Chunk(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	runes := []rune(text)
	if c.Window <= 0 {
		c.Window = 600
	}
	if c.Overlap < 0 || c.Overlap >= c.Window {
		c.Overlap = c.Window / 8
	}
	step := c.Window - c.Overlap
	if step <= 0 {
		step = c.Window
	}
	var out []string
	for start := 0; start < len(runes); start += step {
		end := start + c.Window
		if end > len(runes) {
			end = len(runes)
		}
		chunk := strings.TrimSpace(string(runes[start:end]))
		if chunk != "" {
			out = append(out, chunk)
		}
		if end == len(runes) {
			break
		}
	}
	return out
}
