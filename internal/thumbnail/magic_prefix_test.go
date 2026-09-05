package thumbnail

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
)

type prefixResult struct {
	prefix []byte
	err    error
}

type gatedPrefixReader struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (r *gatedPrefixReader) Read(p []byte) (int, error) {
	if r.calls.Add(1) != 1 {
		return 0, errors.New("unexpected read after cancellation")
	}
	close(r.started)
	<-r.release
	copy(p, []byte("JPEG"))
	return 4, nil
}

func TestReadMagicPrefixCancellationBetweenShortReads(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &gatedPrefixReader{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	result := make(chan prefixResult, 1)
	go func() {
		prefix, err := ReadMagicPrefix(ctx, reader)
		result <- prefixResult{prefix: prefix, err: err}
	}()

	<-reader.started
	cancel()
	close(reader.release)
	got := <-result
	if got.err != context.Canceled {
		t.Fatalf("error = %v, want exact context.Canceled", got.err)
	}
	if !bytes.Equal(got.prefix, []byte("JPEG")) {
		t.Fatalf("prefix = %q, want partial bytes %q", got.prefix, "JPEG")
	}
	if calls := reader.calls.Load(); calls != 1 {
		t.Fatalf("source read calls = %d, want 1", calls)
	}
}

type cancelOnReadPrefixReader struct {
	cancel context.CancelFunc
	calls  atomic.Int32
}

func (r *cancelOnReadPrefixReader) Read(p []byte) (int, error) {
	if r.calls.Add(1) != 1 {
		return 0, errors.New("unexpected second read")
	}
	r.cancel()
	return copy(p, []byte("0123456789ab")), nil
}

func TestReadMagicPrefixChecksContextAfterCompleteRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &cancelOnReadPrefixReader{cancel: cancel}

	prefix, err := ReadMagicPrefix(ctx, reader)
	if err != context.Canceled {
		t.Fatalf("error = %v, want exact context.Canceled", err)
	}
	if !bytes.Equal(prefix, []byte("0123456789ab")) {
		t.Fatalf("prefix = %q, want complete prefix", prefix)
	}
	if calls := reader.calls.Load(); calls != 1 {
		t.Fatalf("source read calls = %d, want 1", calls)
	}
}

type countingPrefixReader struct {
	data  []byte
	calls atomic.Int32
}

func (r *countingPrefixReader) Read(p []byte) (int, error) {
	r.calls.Add(1)
	return bytes.NewReader(r.data).Read(p)
}

func TestReadMagicPrefixCancellationBeforeRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := &countingPrefixReader{data: []byte("not-read")}

	prefix, err := ReadMagicPrefix(ctx, reader)
	if err != context.Canceled {
		t.Fatalf("error = %v, want exact context.Canceled", err)
	}
	if prefix != nil && len(prefix) != 0 {
		t.Fatalf("prefix = %q, want empty prefix", prefix)
	}
	if calls := reader.calls.Load(); calls != 0 {
		t.Fatalf("source read calls = %d, want 0", calls)
	}
}

type sourceErrorPrefixReader struct {
	err   error
	calls atomic.Int32
}

func (r *sourceErrorPrefixReader) Read(p []byte) (int, error) {
	r.calls.Add(1)
	copy(p, []byte("abc"))
	return 3, r.err
}

func TestReadMagicPrefixPreservesShortInputAndSourceErrors(t *testing.T) {
	sourceErr := errors.New("source failed")
	cases := []struct {
		name   string
		reader io.Reader
		prefix []byte
		err    error
	}{
		{
			name:   "empty EOF",
			reader: bytes.NewReader(nil),
			prefix: nil,
			err:    io.EOF,
		},
		{
			name:   "short EOF",
			reader: bytes.NewReader([]byte("abc")),
			prefix: []byte("abc"),
			err:    io.ErrUnexpectedEOF,
		},
		{
			name:   "source error",
			reader: &sourceErrorPrefixReader{err: sourceErr},
			prefix: []byte("abc"),
			err:    sourceErr,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prefix, err := ReadMagicPrefix(context.Background(), tc.reader)
			if !errors.Is(err, tc.err) || err != tc.err {
				t.Fatalf("error = %v, want exact %v", err, tc.err)
			}
			if !bytes.Equal(prefix, tc.prefix) {
				t.Fatalf("prefix = %q, want %q", prefix, tc.prefix)
			}
		})
	}
}

type nilPrefixReader struct{}

func (*nilPrefixReader) Read([]byte) (int, error) {
	panic("typed-nil reader was invoked")
}

func TestReadMagicPrefixRejectsNilReadersBeforeAllocationOrRead(t *testing.T) {
	var typedNil *nilPrefixReader
	for _, tc := range []struct {
		name   string
		reader io.Reader
	}{
		{name: "nil interface", reader: nil},
		{name: "typed nil", reader: typedNil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var prefix []byte
			var err error
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						t.Fatalf("ReadMagicPrefix panicked: %v", recovered)
					}
				}()
				prefix, err = ReadMagicPrefix(context.Background(), tc.reader)
			}()
			if !errors.Is(err, ErrNilSource) {
				t.Fatalf("error = %v, want ErrNilSource", err)
			}
			if prefix != nil {
				t.Fatalf("prefix = %q, want nil", prefix)
			}
		})
	}
}

func TestReadMagicPrefixReadsExactlyOnePrefix(t *testing.T) {
	data := []byte("0123456789abcdef")
	reader := &recordingPrefixReader{data: data}
	prefix, err := ReadMagicPrefix(context.Background(), reader)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if len(prefix) != MagicPrefixLen || !bytes.Equal(prefix, data[:MagicPrefixLen]) {
		t.Fatalf("prefix = %q, want first %d bytes", prefix, MagicPrefixLen)
	}
	remaining, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read remainder: %v", err)
	}
	if !bytes.Equal(remaining, data[MagicPrefixLen:]) {
		t.Fatalf("remainder = %q, want %q", remaining, data[MagicPrefixLen:])
	}
	if reader.calls.Load() == 0 {
		t.Fatal("source was not read")
	}
}

type recordingPrefixReader struct {
	mu    sync.Mutex
	data  []byte
	off   int
	calls atomic.Int32
}

func (r *recordingPrefixReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls.Add(1)
	if r.off == len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}
