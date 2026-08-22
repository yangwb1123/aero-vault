package service

import (
	"context"
	"fmt"
	"net"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// AccessLogEntry contains the request details used by bucket server-access
// logging. It deliberately stays protocol-neutral so the S3 adapter can pass
// the same record to the FileService without exposing repository internals.
type AccessLogEntry struct {
	Method      string
	Key         string
	Status      int
	Latency     time.Duration
	UserAgent   string
	RemoteAddr  string
	RequestID   string
	Referer     string
	Bytes       int
	CompletedAt time.Time
}

// RecordBucketAccessLog writes one S3-compatible access-log object when the
// source bucket has logging enabled. The operation is best-effort from the
// request path: callers should warn on the returned error but must not replace
// an already-written response with an error.
func (s *FileService) RecordBucketAccessLog(
	ctx context.Context,
	tenant, sourceBucket string,
	entry AccessLogEntry,
) error {
	tenant, sourceBucket = defaults(tenant, sourceBucket)
	bucketCfg, err := s.repo.GetBucketConfig(ctx, tenant, sourceBucket)
	if err != nil {
		return fmt.Errorf("get bucket config: %w", err)
	}
	cfg := repository.LoggingConfig{
		Enabled: bucketCfg.LoggingTarget != "",
		Target:  bucketCfg.LoggingTarget,
		Prefix:  bucketCfg.LoggingPrefix,
	}
	if !cfg.Enabled || cfg.Target == "" || cfg.Target == sourceBucket {
		return nil
	}

	completedAt := entry.CompletedAt
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}
	line := formatAccessLog(tenant, sourceBucket, entry, completedAt)
	logKey := accessLogKey(cfg.Prefix, sourceBucket, completedAt, repository.NewVersionID())
	logCtx := access.SystemContext(ctx, tenant)
	_, err = s.Put(
		logCtx,
		tenant,
		cfg.Target,
		logKey,
		strings.NewReader(line),
		int64(len(line)),
		PutOptions{
			ContentType: "text/plain; charset=utf-8",
			Metadata:    map[string]string{"aero-access-log": "true"},
		},
	)
	if err != nil {
		return fmt.Errorf("write access log object: %w", err)
	}
	if err := s.repo.WriteAccessLog(
		ctx,
		tenant,
		sourceBucket,
		entry.Method,
		entry.Key,
		strconv.Itoa(entry.Status),
		strconv.FormatInt(entry.Latency.Milliseconds(), 10),
		entry.UserAgent,
	); err != nil {
		return fmt.Errorf("persist access log: %w", err)
	}
	return nil
}

func accessLogKey(prefix, sourceBucket string, at time.Time, id string) string {
	cleanPrefix := strings.Trim(prefix, "/")
	cleanBucket := strings.NewReplacer("/", "_", "\\", "_").Replace(sourceBucket)
	name := cleanBucket + "-" + at.Format("20060102T150405.000000000Z") + "-" + strings.ReplaceAll(id, "-", "") + ".log"
	parts := []string{cleanBucket, at.Format("2006/01/02/15"), name}
	if cleanPrefix != "" {
		parts = append([]string{cleanPrefix}, parts...)
	}
	return path.Join(parts...)
}

func formatAccessLog(tenant, sourceBucket string, entry AccessLogEntry, at time.Time) string {
	remote := entry.RemoteAddr
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	if remote == "" {
		remote = "-"
	}
	method := strings.ToUpper(strings.TrimSpace(entry.Method))
	if method == "" {
		method = "UNKNOWN"
	}
	opKind := "BUCKET"
	if entry.Key != "" {
		opKind = "OBJECT"
	}
	status := entry.Status
	if status <= 0 {
		status = 200
	}
	latency := entry.Latency.Milliseconds()
	if latency < 0 {
		latency = 0
	}
	return fmt.Sprintf(
		"%s [%s] %s %s REST.%s.%s %s %d %d %d %s %s %s\n",
		tenant,
		at.Format("02/Jan/2006:15:04:05 -0700"),
		remote,
		tenant,
		method,
		opKind,
		strconv.Quote(entry.Key),
		status,
		latency,
		entry.Bytes,
		strconv.Quote(entry.UserAgent),
		strconv.Quote(entry.Referer),
		strconv.Quote(entry.RequestID),
	)
}
