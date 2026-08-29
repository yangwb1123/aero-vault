package thumbnail

import (
	"context"
	"errors"
	"testing"
	"time"
)

func assertNilReaderError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("nil reader returned nil error")
	}
	for _, sentinel := range []error{ErrUnsupported, ErrImageTooLarge, ErrMetadataTooLarge, ErrSourceTooLarge} {
		if errors.Is(err, sentinel) {
			t.Fatalf("nil reader error %v was classified as %v", err, sentinel)
		}
	}
}

func TestGenerateNilReader(t *testing.T) {
	var err error
	var panicValue any
	func() {
		defer func() { panicValue = recover() }()
		_, err = Generate(nil, 16, 16)
	}()
	if panicValue != nil {
		t.Fatalf("Generate(nil) panicked: %v", panicValue)
	}
	assertNilReaderError(t, err)
	assertSlotsReleased(t)
}

func TestGenerateContextNilReader(t *testing.T) {
	slotSaturate()
	defer releaseAndRecoverSlots(t)

	result := make(chan struct {
		err   error
		panic any
	}, 1)
	go func() {
		var err error
		var panicValue any
		func() {
			defer func() { panicValue = recover() }()
			_, err = GenerateContext(context.Background(), nil, 16, 16)
		}()
		result <- struct {
			err   error
			panic any
		}{err: err, panic: panicValue}
	}()

	select {
	case got := <-result:
		if got.panic != nil {
			t.Fatalf("GenerateContext(nil reader) panicked: %v", got.panic)
		}
		assertNilReaderError(t, got.err)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("nil reader waited for a decode slot")
	}
	if held := len(decodeSlots); held != maxConcurrentDecodes {
		t.Fatalf("nil reader changed held slots: got %d, want %d", held, maxConcurrentDecodes)
	}
}
