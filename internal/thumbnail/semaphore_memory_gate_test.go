package thumbnail

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// heldMemoryDecode parks the final source read before returning its bytes to
// the decoder. Large fixtures exceed source read-ahead, so a full pixel frame
// is live then. done publishes err; reached is immutable after ready sends it.
// resume is idempotent so cleanup also safely releases already-finished batches.
type heldMemoryDecode struct {
	source  *bytes.Reader
	ready   chan<- *heldMemoryDecode
	release chan struct{}
	done    chan struct{}
	once    sync.Once
	reached bool
	err     error
}

func newHeldMemoryDecode(fixture []byte, ready chan<- *heldMemoryDecode) *heldMemoryDecode {
	return &heldMemoryDecode{source: bytes.NewReader(fixture), ready: ready, release: make(chan struct{}), done: make(chan struct{})}
}

func (d *heldMemoryDecode) Read(p []byte) (int, error) {
	n, err := d.source.Read(p)
	if n > 0 && d.source.Len() == 0 && !d.reached {
		d.reached = true
		d.ready <- d
		<-d.release
	}
	return n, err
}

func (d *heldMemoryDecode) resume() { d.once.Do(func() { close(d.release) }) }

func TestMemoryDecodeGateBlocksOnlyFinalSourceRead(t *testing.T) {
	ready := make(chan *heldMemoryDecode, 1)
	decoder := newHeldMemoryDecode([]byte("abc"), ready)
	t.Cleanup(decoder.resume)
	var prefix [2]byte
	if n, err := decoder.Read(prefix[:]); n != 2 || err != nil || string(prefix[:]) != "ab" {
		t.Fatalf("prefix read: %q n=%d err=%v", prefix, n, err)
	}
	finished := make(chan struct{})
	go func() {
		var tail [1]byte
		_, _ = decoder.Read(tail[:])
		close(finished)
	}()
	t.Cleanup(func() {
		decoder.resume()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("read did not exit during cleanup")
		}
	})
	select {
	case arrived := <-ready:
		if arrived != decoder || !arrived.reached {
			t.Fatal("wrong decoder gate notification")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("last read never reached gate")
	}
	select {
	case <-finished:
		t.Fatal("final read returned before gate release")
	default:
	}
	decoder.resume()
	decoder.resume()
	assertResumedSourceEOF(t, decoder, finished)
}

func assertResumedSourceEOF(t *testing.T, decoder *heldMemoryDecode, finished <-chan struct{}) {
	t.Helper()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("read did not finish after release")
	}
	var probe [1]byte
	if n, err := decoder.Read(probe[:]); n != 0 || err != io.EOF {
		t.Fatalf("EOF read: n=%d err=%v", n, err)
	}
}

func TestMemoryDecodeEarlyFailureNotifiesWaiter(t *testing.T) {
	ready := startHeldMemoryDecodes(t, bytes.Repeat([]byte("z"), 8192), 1)
	select {
	case decoder := <-ready:
		if decoder.reached {
			t.Fatal("invalid image unexpectedly reached final source read")
		}
		<-decoder.done
		if !errors.Is(decoder.err, ErrUnsupported) {
			t.Fatalf("early error: %v", decoder.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("early decode failure stranded waiter")
	}
}

func TestMemoryDecodeCleanupReleasesGateAndSlot(t *testing.T) {
	var decoder *heldMemoryDecode
	t.Run("abandoned", func(t *testing.T) {
		ready := startHeldMemoryDecodes(t, makePNG(t, 64, 64), 1)
		select {
		case decoder = <-ready:
		case <-time.After(5 * time.Second):
			t.Fatal("source gate not reached")
		}
		// Intentionally leave the read held; subtest cleanup must cancel,
		// release and join it before returning to the parent.
	})
	if decoder == nil {
		t.Fatal("subtest never reached the gate")
	}
	<-decoder.done
	if !errors.Is(decoder.err, context.Canceled) || len(decodeSlots) != 0 {
		t.Fatalf("cleanup err=%v held slots=%d", decoder.err, len(decodeSlots))
	}
}
