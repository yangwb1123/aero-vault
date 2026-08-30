package thumbnail

import (
	"bytes"
	"testing"
)

func TestEffectiveDims(t *testing.T) {
	// The effective-bound rule is the single source of truth shared by
	// generateLocked (the decode pipeline) and the REST handler's cache
	// validator: every requested pair maps to exactly the pair the pipeline
	// applies. All pure comparisons — no arithmetic, no overflow at any int.
	cases := []struct {
		name         string
		w, h         int
		wantW, wantH int
	}{
		{"defaults", 0, 0, DefaultMax, DefaultMax},
		{"negative defaults", -5, 0, DefaultMax, DefaultMax},
		{"in-range identity", 256, 256, 256, 256},
		{"identity at HardMax", 2048, 2048, HardMax, HardMax},
		{"oversized w defaults h", 9999, 0, HardMax, DefaultMax},
		{"oversized both", 9999, 9999, HardMax, HardMax},
		{"small identity", 100, 100, 100, 100},
		{"independent clamp w", 2048, 100, HardMax, 100},
		{"independent clamp h", 100, 9999, 100, HardMax},
		{"max int clamped", int(^uint(0) >> 1), int(^uint(0) >> 1), HardMax, HardMax},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotW, gotH := EffectiveDims(c.w, c.h)
			if gotW != c.wantW || gotH != c.wantH {
				t.Fatalf("EffectiveDims(%d, %d) = (%d, %d), want (%d, %d)",
					c.w, c.h, gotW, gotH, c.wantW, c.wantH)
			}
		})
	}

	// Byte-stability through the refactor: clamping via EffectiveDims must
	// not change any produced bytes — Generate(9999, 9999) and
	// Generate(2048, 2048) already agreed before (both clamped to HardMax)
	// and must still agree after generateLocked delegates to EffectiveDims.
	src := makePNG(t, 3000, 2000)
	oversized, err := Generate(bytes.NewReader(src), 9999, 9999)
	if err != nil {
		t.Fatalf("Generate(9999, 9999): %v", err)
	}
	atHardMax, err := Generate(bytes.NewReader(src), 2048, 2048)
	if err != nil {
		t.Fatalf("Generate(2048, 2048): %v", err)
	}
	if !bytes.Equal(oversized, atHardMax) {
		t.Fatal("Generate(9999, 9999) and Generate(2048, 2048) differ; EffectiveDims changed produced bytes")
	}
}
