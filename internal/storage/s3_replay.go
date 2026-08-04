package storage

import (
	"context"
	"fmt"
	"io"
	"os"
)

// replayableS3Body preserves streaming at the Storage boundary while giving
// the AWS SDK a seekable body that it can rewind when a request is retried.
// CreateTemp uses mode 0600, and cleanup removes the spool after the SDK call.
func replayableS3Body(ctx context.Context, reader io.Reader) (io.Reader, func(), error) {
	if _, ok := reader.(io.ReadSeeker); ok {
		return reader, func() {}, nil
	}

	tmp, err := os.CreateTemp("", "aero-vault-s3-body-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create S3 upload spool: %w", err)
	}
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}
	if _, err := io.Copy(tmp, contextReader{ctx: ctx, reader: reader}); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("spool S3 upload: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("rewind S3 upload spool: %w", err)
	}
	return tmp, cleanup, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
