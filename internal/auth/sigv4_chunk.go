package auth

import (
	"bufio"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const streamingPayload = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"

// decodeStreamingBody rewrites the request body in place when the client used
// SigV4 streaming chunked transfer (the aws-cli default for uploads). The wire
// body is a sequence of signed chunks; we de-chunk it so handlers read the raw
// object bytes. Per-chunk signatures are not re-verified (the seed signature in
// the Authorization header was already verified).
func decodeStreamingBody(req *http.Request) {
	if !strings.EqualFold(req.Header.Get("X-Amz-Content-Sha256"), streamingPayload) {
		return
	}
	req.Body = &chunkedReader{br: bufio.NewReader(req.Body), src: req.Body}
	// The true object length is advertised separately.
	if dl := req.Header.Get("X-Amz-Decoded-Content-Length"); dl != "" {
		if n, err := strconv.ParseInt(dl, 10, 64); err == nil {
			req.ContentLength = n
		}
	} else {
		req.ContentLength = -1
	}
}

// chunkedReader decodes the AWS streaming "aws-chunked" body format:
//
//	<hex-size>;chunk-signature=<sig>\r\n<data>\r\n ... 0;chunk-signature=<sig>\r\n\r\n
type chunkedReader struct {
	br        *bufio.Reader
	src       io.Closer
	remaining int64 // bytes left in the current chunk's data
	done      bool
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	if c.done {
		return 0, io.EOF
	}
	if c.remaining == 0 {
		size, err := c.nextChunkSize()
		if err != nil {
			return 0, err
		}
		if size == 0 {
			c.done = true
			return 0, io.EOF
		}
		c.remaining = size
	}
	n := len(p)
	if int64(n) > c.remaining {
		n = int(c.remaining)
	}
	m, err := c.br.Read(p[:n])
	c.remaining -= int64(m)
	if c.remaining == 0 && err == nil {
		// consume the trailing CRLF after the chunk data
		_, _ = c.br.Discard(2)
	}
	return m, err
}

// nextChunkSize reads a chunk header line and returns the data size.
func (c *chunkedReader) nextChunkSize() (int64, error) {
	line, err := c.br.ReadString('\n')
	if err != nil && line == "" {
		return 0, err
	}
	line = strings.TrimRight(line, "\r\n")
	if i := strings.IndexByte(line, ';'); i >= 0 {
		line = line[:i] // strip ";chunk-signature=..."
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return 0, io.EOF
	}
	return strconv.ParseInt(line, 16, 64)
}

func (c *chunkedReader) Close() error {
	if c.src != nil {
		return c.src.Close()
	}
	return nil
}
