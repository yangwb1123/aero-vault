package thumbnail

import (
	"bytes"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"testing"
)

// sniffWebPBytes is a verified 1×1 RGB WebP produced by Pillow's WEBP
// encoder (opens as WEBP (1,1)); first 12 bytes are RIFF+size+WEBP. The
// server must never decode it, but its magic must be recognized as WebP.
var sniffWebPBytes = func() []byte {
	b, err := hex.DecodeString("524946463c000000574542505650382030000000d001009d012a0100010001402625a00274ba01f80003b000fef2eb7ffcd815cd73eff7ffd2e0fd2e0fd2e0ffd2900000")
	if err != nil {
		panic(err)
	}
	return b
}()

// makeGIF builds a 1×1 GIF with the stdlib encoder — the only supported
// format whose fixture is missing from thumbnail_test.go (makePNG and
// appnPaddedJPEG already exist there).
func makeGIF(t testing.TB) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.RGBA{255, 0, 0, 255}})
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	return buf.Bytes()
}

func TestSniff(t *testing.T) {
	positive := []struct {
		name string
		head []byte
		want Format
	}{
		{"stdlib-encoded png", makePNG(t, 16, 16), FormatPNG},
		{"stdlib-encoded jpeg", appnPaddedJPEG(t, 0), FormatJPEG},
		{"stdlib-encoded gif", makeGIF(t), FormatGIF},
		{"webp fixture", sniffWebPBytes, FormatWebP},
		{"bare jpeg magic", []byte{0xFF, 0xD8}, FormatJPEG},
		{"bare png magic", []byte{0x89, 'P', 'N', 'G'}, FormatPNG},
		{"gif87a", []byte("GIF87a"), FormatGIF},
		{"gif89a", []byte("GIF89a"), FormatGIF},
		{"bare webp magic", append(append([]byte("RIFF"), 0x3c, 0, 0, 0), []byte("WEBP")...), FormatWebP},
		{"jpeg magic with trailing garbage", append([]byte{0xFF, 0xD8}, bytes.Repeat([]byte{0xAA}, 32)...), FormatJPEG},
	}
	negative := []struct {
		name string
		head []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"single 0xff", []byte{0xFF}},
		{"single 0x89", []byte{0x89}},
		{"single G", []byte{'G'}},
		{"single R", []byte{'R'}},
		{"truncated gif", []byte("GI")},
		{"riff only", []byte("RIFF")},
		{"riff+size no webp", append([]byte("RIFF"), 0, 0, 0, 0)},
		{"riff+size+truncated webp", []byte("RIFF\x00\x00\x00\x00WEB")},
		{"text", []byte("hello")},
		{"random 12 bytes", []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C}},
	}
	for _, tc := range positive {
		t.Run("accept "+tc.name, func(t *testing.T) {
			if got := Sniff(tc.head); got != tc.want {
				t.Fatalf("Sniff(%x) = %v, want %v", tc.head, got, tc.want)
			}
		})
	}
	for _, tc := range negative {
		t.Run("reject "+tc.name, func(t *testing.T) {
			if got := Sniff(tc.head); got != FormatUnknown {
				t.Fatalf("Sniff(%x) = %v, want FormatUnknown", tc.head, got)
			}
		})
	}
	// Determinism and idempotence: a pure function must agree on every call
	// for the same input, including the WebP fixture (which carries data past
	// the 12-byte magic window).
	for i := 0; i < 100; i++ {
		if got := Sniff(sniffWebPBytes); got != FormatWebP {
			t.Fatalf("Sniff(webp) iteration %d = %v, want FormatWebP", i, got)
		}
	}
}

func TestSniffRejectsFakeGIFPrefixes(t *testing.T) {
	cases := []struct {
		name string
		head []byte
	}{
		{"too short", []byte("GIF")},
		{"wrong version", []byte("GIF90a")},
		{"wrong 87 suffix", []byte("GIF87b")},
		{"wrong 89 suffix", []byte("GIF89b")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sniff(tc.head); got != FormatUnknown {
				t.Fatalf("Sniff(%q) = %v, want FormatUnknown", tc.head, got)
			}
		})
	}
}

// TestAdmitByMagicRejectsFakeGIFPrefixes ensures invalid GIF-looking bytes
// use the existing client-argument rejection path rather than admission.
func TestAdmitByMagicRejectsFakeGIFPrefixes(t *testing.T) {
	for _, head := range [][]byte{[]byte("GIF"), []byte("GIF90a"), []byte("GIF87b"), []byte("GIF89b")} {
		replay, err := AdmitByMagic(head)
		if !errors.Is(err, ErrNotAnImage) {
			t.Fatalf("AdmitByMagic(%q) error=%v, want ErrNotAnImage", head, err)
		}
		if replay != nil {
			t.Fatalf("AdmitByMagic(%q) replay=%q, want nil", head, replay)
		}
	}
}

// TestAdmitByMagic pins the extracted admission decision: the decodable
// formats replay the head exactly as read; WebP is the server-capability
// rejection (ErrUnsupportedFormat); unknown and too-short bytes are the
// client-argument rejection (ErrNotAnImage). The replay is byte-identical —
// the pipeline must observe the full object, magic included.
func TestAdmitByMagic(t *testing.T) {
	jpegHead := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x42}
	pngHead := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A}
	gifHead := []byte{'G', 'I', 'F', '8', '9', 'a'}
	webpHead := []byte("RIFF\x24\x00\x00\x00WEBPVP8 ")
	cases := []struct {
		name string
		head []byte
		want error // nil = admitted
	}{
		{"jpeg magic", jpegHead, nil},
		{"png magic", pngHead, nil},
		{"gif magic", gifHead, nil},
		{"webp magic rejected", webpHead, ErrUnsupportedFormat},
		{"unknown bytes", []byte("hello world"), ErrNotAnImage},
		{"empty head", nil, ErrNotAnImage},
		{"truncated jpeg", []byte{0xFF}, ErrNotAnImage},
		{"truncated png", []byte{0x89, 'P'}, ErrNotAnImage},
		{"truncated gif", []byte{'G', 'I'}, ErrNotAnImage},
		{"truncated riff", webpHead[:8], ErrNotAnImage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			replay, err := AdmitByMagic(tc.head)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("AdmitByMagic(% x) = err %v, want admission", tc.head, err)
				}
				if !bytes.Equal(replay, tc.head) {
					t.Fatalf("replay % x, want the head % x byte-identical (pipeline must see the magic)", replay, tc.head)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("AdmitByMagic(% x) = err %v, want %v", tc.head, err, tc.want)
			}
			if replay != nil {
				t.Fatalf("rejected admission returned a replay (% x)", replay)
			}
		})
	}
}
