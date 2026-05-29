package ai

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// --- Chunker ---

func TestNewChunkerDefaults(t *testing.T) {
	c := NewChunker()
	if c.Window != 600 || c.Overlap != 80 {
		t.Fatalf("NewChunker defaults: got Window=%d Overlap=%d, want 600/80", c.Window, c.Overlap)
	}
}

func TestChunkerEmptyAndShort(t *testing.T) {
	c := NewChunker()
	if got := c.Chunk(""); got != nil {
		t.Fatalf("empty text: want nil, got %v", got)
	}
	if got := c.Chunk("   \n\t  "); got != nil {
		t.Fatalf("whitespace-only: want nil, got %v", got)
	}
	short := "hello world"
	got := c.Chunk(short)
	if len(got) != 1 || got[0] != short {
		t.Fatalf("short text: want single chunk %q, got %v", short, got)
	}
}

func TestChunkerExactWindowIsOneChunk(t *testing.T) {
	c := &Chunker{Window: 10, Overlap: 2}
	text := strings.Repeat("a", 10)
	got := c.Chunk(text)
	if len(got) != 1 {
		t.Fatalf("exact-window text: want 1 chunk, got %d (%v)", len(got), got)
	}
	if got[0] != text {
		t.Fatalf("exact-window chunk content mismatch: got %q", got[0])
	}
}

func TestChunkerOverlapAndCount(t *testing.T) {
	// Window 10, overlap 4 => step 6. 26 runes ("abc...xyz").
	c := &Chunker{Window: 10, Overlap: 4}
	letters := "abcdefghijklmnopqrstuvwxyz" // 26 runes
	got := c.Chunk(letters)

	// starts: 0,6,12,18. The chunk at start=18 spans [18:26], which reaches the
	// end of the input, so the loop breaks before start=24 — no trailing "yz"
	// chunk is emitted.
	want := []string{
		"abcdefghij", // 0:10
		"ghijklmnop", // 6:16
		"mnopqrstuv", // 12:22
		"stuvwxyz",   // 18:26 (trimmed to len, ends at len -> break)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("overlap chunking mismatch:\n got  %v\n want %v", got, want)
	}

	// Verify the overlap invariant: consecutive full-width chunks share
	// `overlap` runes. chunk[0][6:] == chunk[1][:4].
	if got[0][6:] != got[1][:4] {
		t.Fatalf("expected 4-rune overlap between chunk0 and chunk1: %q vs %q", got[0][6:], got[1][:4])
	}
}

func TestChunkerUTF8NoMidCodepointBreak(t *testing.T) {
	// CJK runes are multi-byte; chunker works in runes so each chunk must be
	// valid UTF-8 and contain whole characters.
	c := &Chunker{Window: 3, Overlap: 1}
	text := "你好世界朋友" // 6 runes
	got := c.Chunk(text)
	if len(got) == 0 {
		t.Fatal("expected chunks for CJK text")
	}
	for i, ch := range got {
		runes := []rune(ch)
		if len(runes) > 3 {
			t.Fatalf("chunk %d exceeds window in runes: %q", i, ch)
		}
		// re-encoding round-trips => no mid-codepoint break
		if string(runes) != ch {
			t.Fatalf("chunk %d not valid rune-aligned text: %q", i, ch)
		}
	}
}

func TestChunkerSanitizesBadParams(t *testing.T) {
	// Window<=0 resets to 600; Overlap>=Window resets to Window/8.
	c := &Chunker{Window: 0, Overlap: 0}
	_ = c.Chunk("some text here")
	if c.Window != 600 {
		t.Fatalf("Window<=0 should reset to 600, got %d", c.Window)
	}

	c2 := &Chunker{Window: 100, Overlap: 200}
	_ = c2.Chunk(strings.Repeat("x", 50))
	if c2.Overlap != 100/8 {
		t.Fatalf("Overlap>=Window should reset to Window/8=%d, got %d", 100/8, c2.Overlap)
	}
}

// --- HashEmbedder ---

func TestHashEmbedderDimensionsAndName(t *testing.T) {
	e := NewHashEmbedder(128)
	if e.Dimensions() != 128 {
		t.Fatalf("Dimensions: want 128, got %d", e.Dimensions())
	}
	if e.Name() != "hash-128" {
		t.Fatalf("Name: want hash-128, got %q", e.Name())
	}
	// dim<=0 falls back to 256.
	if d := NewHashEmbedder(0).Dimensions(); d != 256 {
		t.Fatalf("dim<=0 fallback: want 256, got %d", d)
	}
	if d := NewHashEmbedder(-5).Dimensions(); d != 256 {
		t.Fatalf("negative dim fallback: want 256, got %d", d)
	}
}

func TestHashEmbedderDeterministicAndShape(t *testing.T) {
	e := NewHashEmbedder(64)
	texts := []string{"the quick brown fox", "the quick brown fox", "a different string"}
	v1, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(v1) != 3 {
		t.Fatalf("want 3 vectors, got %d", len(v1))
	}
	for i, v := range v1 {
		if len(v) != 64 {
			t.Fatalf("vector %d dim: want 64, got %d", i, len(v))
		}
	}
	// Identical inputs => identical vectors.
	if !reflect.DeepEqual(v1[0], v1[1]) {
		t.Fatalf("identical inputs produced different vectors")
	}
	// Different input => different vector.
	if reflect.DeepEqual(v1[0], v1[2]) {
		t.Fatalf("different inputs produced identical vectors")
	}

	// Stable across calls.
	v2, _ := e.Embed(context.Background(), []string{"the quick brown fox"})
	if !reflect.DeepEqual(v1[0], v2[0]) {
		t.Fatalf("embedding not stable across calls")
	}
}

func TestHashEmbedderL2Normalized(t *testing.T) {
	e := NewHashEmbedder(256)
	vecs, err := e.Embed(context.Background(), []string{"normalize me please, this is a longer sentence with words"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	var sumSq float64
	for _, x := range vecs[0] {
		sumSq += float64(x) * float64(x)
	}
	// L2 norm should be ~1 (non-empty input).
	if sumSq < 0.99 || sumSq > 1.01 {
		t.Fatalf("vector not L2-normalized: sum of squares = %f", sumSq)
	}
}

func TestHashEmbedderShortInputDoesNotPanic(t *testing.T) {
	// Inputs shorter than the 5-rune shingle window are padded; ensure no panic
	// and the dimension is still correct.
	e := NewHashEmbedder(32)
	vecs, err := e.Embed(context.Background(), []string{"", "ab", "x"})
	if err != nil {
		t.Fatalf("embed short: %v", err)
	}
	for i, v := range vecs {
		if len(v) != 32 {
			t.Fatalf("short input %d: dim want 32 got %d", i, len(v))
		}
	}
}

// --- tokenize (bm25.go) ---

func TestTokenize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"lowercases", "Hello WORLD", []string{"hello", "world"}},
		{"drops single chars", "a bb c dd", []string{"bb", "dd"}},
		{"splits punctuation", "foo,bar.baz!qux", []string{"foo", "bar", "baz", "qux"}},
		{"keeps digits", "abc 123 4", []string{"abc", "123"}},
		{"empty", "", nil},
		{"only short/punct", "a , . !", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokenize(tt.in)
			if len(tt.want) == 0 && len(got) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("tokenize(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// --- PIIDetector ---

func TestPIIScan(t *testing.T) {
	p := NewPIIDetector()
	text := "Contact alice@example.com or bob@test.org. SSN 123-45-6789. IP 192.168.0.1"
	scan := p.Scan(text)
	if scan["email"] != 2 {
		t.Fatalf("email count: want 2, got %d (%v)", scan["email"], scan)
	}
	if scan["ssn"] != 1 {
		t.Fatalf("ssn count: want 1, got %d (%v)", scan["ssn"], scan)
	}
	if scan["ip_v4"] < 1 {
		t.Fatalf("expected at least one ip_v4 hit, got %v", scan)
	}
	// Clean text yields empty map.
	if got := p.Scan("just some harmless words"); len(got) != 0 {
		t.Fatalf("clean text should have no PII, got %v", got)
	}
}

func TestPIIRedactAll(t *testing.T) {
	p := NewPIIDetector()
	in := "mail alice@example.com ssn 123-45-6789"
	out := p.Redact(in, nil)
	if strings.Contains(out, "alice@example.com") {
		t.Fatalf("email not redacted: %q", out)
	}
	if strings.Contains(out, "123-45-6789") {
		t.Fatalf("ssn not redacted: %q", out)
	}
	if !strings.Contains(out, "[REDACTED-EMAIL]") {
		t.Fatalf("expected email placeholder, got %q", out)
	}
}

func TestPIIRedactSelective(t *testing.T) {
	p := NewPIIDetector()
	in := "mail alice@example.com ssn 123-45-6789"
	// Only redact email; SSN must survive.
	out := p.Redact(in, map[string]bool{"email": true})
	if strings.Contains(out, "alice@example.com") {
		t.Fatalf("email should be redacted: %q", out)
	}
	if !strings.Contains(out, "123-45-6789") {
		t.Fatalf("ssn should survive selective redaction: %q", out)
	}
}

func TestMapPII(t *testing.T) {
	if got := MapPII(map[string]int{}); got != "" {
		t.Fatalf("empty scan should map to empty string, got %q", got)
	}
	// Single entry is deterministic.
	if got := MapPII(map[string]int{"email": 3}); got != "email=3" {
		t.Fatalf("MapPII single: want email=3, got %q", got)
	}
	// Multi-entry: order is map-iteration dependent, so assert membership.
	got := MapPII(map[string]int{"email": 2, "ssn": 1})
	parts := strings.Split(got, ",")
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %q", got)
	}
	set := map[string]bool{parts[0]: true, parts[1]: true}
	if !set["email=2"] || !set["ssn=1"] {
		t.Fatalf("MapPII multi missing entries: %q", got)
	}
}

// --- DefaultExtractor ---

func TestDefaultExtractorContentTypes(t *testing.T) {
	e := NewDefaultExtractor()
	if e.MaxBytes != 4<<20 {
		t.Fatalf("default MaxBytes want 4MB, got %d", e.MaxBytes)
	}
	supported := []string{
		"text/plain", "text/markdown; charset=utf-8", "application/json",
		"application/xml", "application/yaml", "application/x-yaml",
		"application/foo+xml", "application/bar+json", "",
		"TEXT/PLAIN", // case-insensitive
	}
	for _, ct := range supported {
		got, err := e.Extract(context.Background(), ct, strings.NewReader("body text"))
		if err != nil {
			t.Fatalf("content-type %q: unexpected error %v", ct, err)
		}
		if got != "body text" {
			t.Fatalf("content-type %q: want passthrough body, got %q", ct, got)
		}
	}
}

func TestDefaultExtractorUnsupported(t *testing.T) {
	e := NewDefaultExtractor()
	for _, ct := range []string{"application/pdf", "image/png", "application/octet-stream", "audio/mpeg"} {
		_, err := e.Extract(context.Background(), ct, strings.NewReader("\x00\x01binary"))
		if err != ErrUnsupported {
			t.Fatalf("content-type %q: want ErrUnsupported, got %v", ct, err)
		}
	}
}

func TestDefaultExtractorMaxBytesCap(t *testing.T) {
	e := &DefaultExtractor{MaxBytes: 5}
	got, err := e.Extract(context.Background(), "text/plain", strings.NewReader("0123456789"))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got != "01234" {
		t.Fatalf("MaxBytes cap: want first 5 bytes, got %q", got)
	}
}

// --- Indexer EncodeObjectID/DecodeObjectID ---

func TestEncodeDecodeObjectIDRoundTrip(t *testing.T) {
	for _, id := range []int64{1, 42, 9999, 1 << 40} {
		payload := EncodeObjectID(id)
		got, err := DecodeObjectID(payload)
		if err != nil {
			t.Fatalf("decode id=%d: %v", id, err)
		}
		if got != id {
			t.Fatalf("round-trip id mismatch: want %d, got %d", id, got)
		}
	}
}

func TestDecodeObjectIDErrors(t *testing.T) {
	if _, err := DecodeObjectID("not json"); err == nil {
		t.Fatal("expected error decoding invalid JSON")
	}
	// Valid JSON but missing/zero object_id.
	if _, err := DecodeObjectID(`{"object_id":0}`); err == nil {
		t.Fatal("expected error for zero object_id")
	}
	if _, err := DecodeObjectID(`{}`); err == nil {
		t.Fatal("expected error for empty object")
	}
}

// --- HeuristicReranker ---

func TestHeuristicRerankerOrdersByTermOverlap(t *testing.T) {
	r := HeuristicReranker{}
	if r.Name() != "heuristic" {
		t.Fatalf("Name: want heuristic, got %q", r.Name())
	}
	hits := []Hit{
		{ChunkID: 1, Chunk: "nothing relevant in this text at all"},
		{ChunkID: 2, Chunk: "golang testing golang patterns"}, // 2 query-term matches
		{ChunkID: 3, Chunk: "a little golang here"},           // 1 match
	}
	out, err := r.Rerank(context.Background(), "golang testing", hits, 0)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("topK=0 should keep all, got %d", len(out))
	}
	if out[0].ChunkID != 2 {
		t.Fatalf("expected chunk 2 (most overlap) first, got %d", out[0].ChunkID)
	}
	if out[len(out)-1].ChunkID != 1 {
		t.Fatalf("expected chunk 1 (no overlap) last, got %d", out[len(out)-1].ChunkID)
	}
}

func TestHeuristicRerankerTopKTrim(t *testing.T) {
	r := HeuristicReranker{}
	hits := []Hit{
		{ChunkID: 1, Chunk: "alpha beta gamma"},
		{ChunkID: 2, Chunk: "alpha"},
		{ChunkID: 3, Chunk: "beta gamma delta"},
	}
	out, err := r.Rerank(context.Background(), "alpha beta", hits, 2)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("topK=2 should trim to 2, got %d", len(out))
	}
}

func TestHeuristicRerankerShorterChunkBonus(t *testing.T) {
	// With equal term overlap, the shorter chunk gets a smaller length penalty
	// and should rank higher.
	r := HeuristicReranker{}
	long := "match " + strings.Repeat("filler ", 500)
	hits := []Hit{
		{ChunkID: 1, Chunk: long},
		{ChunkID: 2, Chunk: "match"},
	}
	out, _ := r.Rerank(context.Background(), "match", hits, 0)
	if out[0].ChunkID != 2 {
		t.Fatalf("expected shorter matching chunk first, got %d", out[0].ChunkID)
	}
}
