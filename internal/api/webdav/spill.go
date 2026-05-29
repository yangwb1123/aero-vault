package webdav

import (
	"io"
	"os"
)

// spillThreshold is the number of bytes kept in memory before a spillBuffer
// switches to an on-disk temp file. Beyond this size, data is streamed to a
// temp file so that arbitrarily large objects do not pin the whole payload in
// RAM. It is a package var (not a const) so tests can lower it to exercise the
// spill path cheaply; production code never mutates it.
var spillThreshold int64 = 8 << 20 // 8 MiB

// spillBuffer is an io.Reader/io.Writer/io.Seeker/io.Closer that keeps small
// payloads in memory and transparently spills to a temp file once it grows
// beyond spillThreshold. It is used by both the WebDAV read and write paths so
// that Range requests (which need Seek) work without buffering the whole
// object in memory.
//
// A spillBuffer is single-cursor: reads and writes share one offset, exactly
// like an *os.File or *bytes.Reader. The WebDAV adapter writes-then-rewinds
// (write path) or copies-then-rewinds (read path), so this is sufficient.
type spillBuffer struct {
	mem  []byte   // in-memory backing while size <= spillThreshold
	file *os.File // temp file backing once spilled; nil while in memory
	off  int64    // current read/write offset
	size int64    // logical length of the buffer
}

func newSpillBuffer() *spillBuffer { return &spillBuffer{} }

// spilled reports whether the buffer has switched to its temp file.
func (s *spillBuffer) spilled() bool { return s.file != nil }

// promote moves the in-memory contents to a temp file. Called when a write
// would push the logical size past spillThreshold.
func (s *spillBuffer) promote() error {
	f, err := os.CreateTemp("", "aero-dav-*")
	if err != nil {
		return err
	}
	if len(s.mem) > 0 {
		if _, err := f.Write(s.mem); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return err
		}
	}
	// Position the file cursor at the current logical offset so subsequent
	// reads/writes continue seamlessly.
	if _, err := f.Seek(s.off, io.SeekStart); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return err
	}
	s.file = f
	s.mem = nil
	return nil
}

func (s *spillBuffer) Write(p []byte) (int, error) {
	if s.spilled() {
		n, err := s.file.Write(p)
		s.off += int64(n)
		if s.off > s.size {
			s.size = s.off
		}
		return n, err
	}
	if s.off+int64(len(p)) > spillThreshold {
		if err := s.promote(); err != nil {
			return 0, err
		}
		return s.Write(p)
	}
	// Grow/overwrite the in-memory slice at the current offset.
	end := s.off + int64(len(p))
	if end > int64(len(s.mem)) {
		grown := make([]byte, end)
		copy(grown, s.mem)
		s.mem = grown
	}
	copy(s.mem[s.off:end], p)
	s.off = end
	if s.off > s.size {
		s.size = s.off
	}
	return len(p), nil
}

func (s *spillBuffer) Read(p []byte) (int, error) {
	if s.spilled() {
		n, err := s.file.Read(p)
		s.off += int64(n)
		return n, err
	}
	if s.off >= s.size {
		return 0, io.EOF
	}
	n := copy(p, s.mem[s.off:s.size])
	s.off += int64(n)
	return n, nil
}

func (s *spillBuffer) Seek(offset int64, whence int) (int64, error) {
	if s.spilled() {
		abs, err := s.file.Seek(offset, whence)
		if err == nil {
			s.off = abs
		}
		return abs, err
	}
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = s.off + offset
	case io.SeekEnd:
		abs = s.size + offset
	default:
		return 0, os.ErrInvalid
	}
	if abs < 0 {
		return 0, os.ErrInvalid
	}
	s.off = abs
	return abs, nil
}

// Close removes the temp file (if any) and releases the in-memory buffer.
// It is always safe to call multiple times.
func (s *spillBuffer) Close() error {
	s.mem = nil
	if s.file == nil {
		return nil
	}
	f := s.file
	s.file = nil
	name := f.Name()
	err := f.Close()
	if rmErr := os.Remove(name); rmErr != nil && err == nil {
		err = rmErr
	}
	return err
}

// Len reports the logical length of the buffer.
func (s *spillBuffer) Len() int64 { return s.size }

// fill copies the contents of r into the buffer and rewinds the cursor to the
// start, ready to be read back. r is fully consumed (but not closed).
func (s *spillBuffer) fill(r io.Reader) error {
	if _, err := io.Copy(s, r); err != nil {
		return err
	}
	_, err := s.Seek(0, io.SeekStart)
	return err
}

// Ensure spillBuffer satisfies the interfaces the WebDAV adapter needs.
var (
	_ io.Reader = (*spillBuffer)(nil)
	_ io.Writer = (*spillBuffer)(nil)
	_ io.Seeker = (*spillBuffer)(nil)
	_ io.Closer = (*spillBuffer)(nil)
)
