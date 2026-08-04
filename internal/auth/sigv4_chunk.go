package auth

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const (
	streamingPayload = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	chunkAlgorithm   = "AWS4-HMAC-SHA256-PAYLOAD"
	maxChunkSize     = 16 << 20
)

func (v *SigV4Verifier) prepareStreamingBody(req *http.Request) error {
	credential, _, seedSignature, err := parseAuthHeader(req.Header.Get("Authorization"))
	if err != nil {
		return err
	}
	accessKey, scope, err := splitCredential(credential)
	if err != nil {
		return err
	}
	cred, ok := v.creds[accessKey]
	if !ok {
		return errors.New("sigv4: unknown access key")
	}
	parts := strings.Split(scope, "/")
	req.Body = &verifiedChunkReader{
		reader:      bufio.NewReader(req.Body),
		source:      req.Body,
		signingKey:  deriveSigningKey(cred.secret, parts[0], parts[1]),
		amzDate:     req.Header.Get("X-Amz-Date"),
		scope:       scope,
		previousSig: seedSignature,
	}
	if value := req.Header.Get("X-Amz-Decoded-Content-Length"); value != "" {
		length, err := strconv.ParseInt(value, 10, 64)
		if err != nil || length < 0 {
			return errors.New("sigv4: invalid decoded content length")
		}
		req.ContentLength = length
	} else {
		req.ContentLength = -1
	}
	return nil
}

type verifiedChunkReader struct {
	reader      *bufio.Reader
	source      io.Closer
	current     *bytes.Reader
	signingKey  []byte
	amzDate     string
	scope       string
	previousSig string
	done        bool
}

func (r *verifiedChunkReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for r.current == nil || r.current.Len() == 0 {
		if r.done {
			return 0, io.EOF
		}
		if err := r.loadChunk(); err != nil {
			return 0, err
		}
	}
	return r.current.Read(p)
}

func (r *verifiedChunkReader) loadChunk() error {
	size, signature, err := r.readChunkHeader()
	if err != nil {
		return err
	}
	if size > maxChunkSize {
		return errors.New("sigv4: streaming chunk too large")
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(r.reader, data); err != nil {
		return err
	}
	if err := consumeCRLF(r.reader); err != nil {
		return err
	}
	expected := streamingChunkSignature(
		r.signingKey, r.amzDate, r.scope, r.previousSig, data,
	)
	if !hmac.Equal([]byte(expected), []byte(strings.ToLower(signature))) {
		return errors.New("sigv4: streaming chunk signature mismatch")
	}
	r.previousSig = expected
	r.current = bytes.NewReader(data)
	r.done = size == 0
	return nil
}

func (r *verifiedChunkReader) readChunkHeader() (int64, string, error) {
	line, err := r.reader.ReadString('\n')
	if err != nil {
		return 0, "", err
	}
	if len(line) > 4096 || !strings.HasSuffix(line, "\r\n") {
		return 0, "", errors.New("sigv4: malformed streaming chunk header")
	}
	fields := strings.Split(strings.TrimSuffix(line, "\r\n"), ";")
	if len(fields) != 2 || !strings.HasPrefix(fields[1], "chunk-signature=") {
		return 0, "", errors.New("sigv4: missing streaming chunk signature")
	}
	size, err := strconv.ParseInt(fields[0], 16, 64)
	signature := strings.TrimPrefix(fields[1], "chunk-signature=")
	if err != nil || size < 0 || len(signature) != sha256HexLength {
		return 0, "", errors.New("sigv4: malformed streaming chunk header")
	}
	if _, err := hex.DecodeString(signature); err != nil {
		return 0, "", errors.New("sigv4: malformed streaming chunk signature")
	}
	return size, signature, nil
}

func consumeCRLF(reader io.Reader) error {
	var suffix [2]byte
	if _, err := io.ReadFull(reader, suffix[:]); err != nil {
		return err
	}
	if suffix != [2]byte{'\r', '\n'} {
		return errors.New("sigv4: malformed streaming chunk terminator")
	}
	return nil
}

func streamingChunkSignature(
	signingKey []byte, amzDate, scope, previousSignature string, data []byte,
) string {
	emptyHash := sha256.Sum256(nil)
	dataHash := sha256.Sum256(data)
	stringToSign := strings.Join([]string{
		chunkAlgorithm,
		amzDate,
		scope,
		previousSignature,
		hex.EncodeToString(emptyHash[:]),
		hex.EncodeToString(dataHash[:]),
	}, "\n")
	return hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))
}

func (r *verifiedChunkReader) Close() error {
	if r.source != nil {
		return r.source.Close()
	}
	return nil
}
