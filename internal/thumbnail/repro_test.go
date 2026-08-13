package thumbnail

// Self-verifying reproduction of every fuzzer-discovered crasher pinned in
// testdata/fuzz (F-2): each corpus entry is decoded from the Go fuzz corpus
// format and executed through the target function. The default gate (amd64)
// runs these; the CI 386 leg executes the same seeds under GOARCH=386, so a
// crash-class regression in either architecture fails the suite instead of
// being amd64-vacuous.
import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fuzzCorpusInputs decodes the Go fuzz corpus files under testdata/fuzz for
// the given target (one []byte input per file; "go test fuzz v1" header
// followed by a quoted []byte literal).
func fuzzCorpusInputs(t *testing.T, target string) [][]byte {
	t.Helper()
	dir := filepath.Join("testdata", "fuzz", target)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skipf("no pinned crashers for %s", target)
		}
		t.Fatalf("read corpus dir %s: %v", dir, err)
	}
	var out [][]byte
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("open corpus %s: %v", e.Name(), err)
		}
		br := bufio.NewReader(f)
		header, err := br.ReadString('\n')
		if err != nil {
			f.Close()
			t.Fatalf("read corpus header %s: %v", e.Name(), err)
		}
		rest, err := io.ReadAll(br)
		f.Close()
		if err != nil {
			t.Fatalf("read corpus body %s: %v", e.Name(), err)
		}
		if !strings.HasPrefix(header, "go test fuzz v1") {
			t.Fatalf("corpus %s: unexpected header %q", e.Name(), header)
		}
		// Format: []byte("...") possibly multi-line — decode with
		// fmt.Sscanf on the literal is fragile; instead execute via the
		// fuzz machinery itself: parse the quoted string the same way Go
		// writes it (strconv.Unquote over the raw literal text).
		lit := strings.TrimSpace(string(rest))
		if !strings.HasPrefix(lit, "[]byte(") || !strings.HasSuffix(lit, ")") {
			t.Fatalf("corpus %s: unexpected literal %q", e.Name(), lit[:min(len(lit), 40)])
		}
		quoted := lit[len("[]byte(") : len(lit)-1]
		data, uerr := strconvUnquote(quoted)
		if uerr != nil {
			t.Fatalf("corpus %s: unquote: %v", e.Name(), uerr)
		}
		out = append(out, data)
	}
	if len(out) == 0 {
		t.Fatalf("corpus dir %s: no inputs", dir)
	}
	return out
}

func strconvUnquote(s string) ([]byte, error) {
	// The corpus format writes the literal as a Go-quoted string;
	// strconv.Unquote handles every escape (\xNN, \b, \n, ...) natively.
	u, err := strconv.Unquote(s)
	if err != nil {
		return nil, err
	}
	return []byte(u), nil
}

// TestFuzzCorpusReproduction executes every pinned corpus entry through its
// target: a regression in the crash class panics or misbehaves here, in the
// default gate, on the 386 leg, and in the amd64 fuzz seed phase.
func TestFuzzCorpusReproduction(t *testing.T) {
	cases := []struct {
		target string
		run    func([]byte)
	}{
		{"FuzzExifOrientation", func(b []byte) {
			if got := exifOrientation(b); got < 1 || got > 8 {
				t.Fatalf("exifOrientation = %d, want 1..8", got)
			}
		}},
		{"FuzzProgressiveJPEG", func(b []byte) { _ = progressiveJPEG(b) }},
	}
	for _, tc := range cases {
		for i, input := range fuzzCorpusInputs(t, tc.target) {
			t.Run(fmt.Sprintf("%s/%d", tc.target, i), func(t *testing.T) {
				tc.run(input)
			})
		}
	}
}
