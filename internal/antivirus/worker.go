package antivirus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// JobScan is the job type for asynchronous object scanning.
const JobScan = "virus_scan"

// Tag keys written to scanned objects.
const (
	TagStatus    = "av_status"    // clean | infected
	TagSignature = "av_signature" // threat name when infected
)

// SystemActor is the audit/subject identity pinned on every controller call
// made by antivirus jobs (FR-4). It is constant-derived — never attacker
// input — and runs with Kind PrincipalSystem, which already bypasses resource
// ACLs for the job handler (authorizer.go), so this changes attribution only,
// never the authorization decision. Quarantine audit rows are identified by
// (action=file.delete, detail=av_infected), not by actor alone: an admin
// issued JWT may legitimately carry a "system:"-prefixed subject.
//
// It aliases access.SystemActorAntivirus — the single source of truth for the
// one system principal exempt from the fail-closed delete gate
// (access.IsSystemDeleteExempt).
const SystemActor = access.SystemActorAntivirus

// maxSignatureBytes bounds the scanner-reported threat name before it reaches
// the quarantine transaction (both the tag write and the notify@1.1 payload).
// An unbounded signature from a misbehaving HTTPScanner would otherwise wedge
// quarantine into permanent validation rollback/retry (availability guard,
// not a trust boundary).
const maxSignatureBytes = 4 << 10 // 4 KiB

// Enqueuer is the slice of the job queue the bridge needs.
type Enqueuer interface {
	Enqueue(ctx context.Context, j repository.Job) (int64, bool, error)
}

// ObjectController is the FileService slice needed by antivirus jobs.
type ObjectController interface {
	SetObjectTagsByID(ctx context.Context, objectID int64, tags map[string]string) error
	// QuarantineObjectByID soft-deletes one exact version and atomically
	// writes its audit row + outbox facts; signature is the scanner-reported
	// threat name carried in the notify@1.1 payload.
	QuarantineObjectByID(ctx context.Context, objectID int64, signature string) error
}

type scanPayload struct {
	ObjectID int64 `json:"object_id"`
}

// EncodeObjectID builds a virus_scan job payload.
func EncodeObjectID(id int64) string {
	b, _ := json.Marshal(scanPayload{ObjectID: id})
	return string(b)
}

// DecodeObjectID parses a virus_scan job payload.
func DecodeObjectID(payload string) (int64, error) {
	var p scanPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return 0, fmt.Errorf("decode virus_scan payload: %w", err)
	}
	if p.ObjectID == 0 {
		return 0, errors.New("payload missing object_id")
	}
	return p.ObjectID, nil
}

// Worker scans objects and records the verdict. It is both the job handler
// (ScanObjectByID) and the event→job bridge (Run).
type Worker struct {
	repo       repository.Repository
	store      storage.Storage
	scanner    Scanner
	queue      Enqueuer
	objects    ObjectController
	quarantine bool
	logger     *slog.Logger
}

func NewWorker(repo repository.Repository, store storage.Storage, scanner Scanner, queue Enqueuer, quarantine bool, logger *slog.Logger) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{repo: repo, store: store, scanner: scanner, queue: queue, quarantine: quarantine, logger: logger}
}

// WithObjectController routes worker mutations through FileService.
func (w *Worker) WithObjectController(objects ObjectController) *Worker {
	w.objects = objects
	return w
}

// scanCounter counts every byte read from the wrapped reader. Used on the
// HTTPScanner path (worker.go) to detect a remote engine that responded before
// the whole object was consumed. The counter is atomic because the HTTP
// transport may read the body from a background goroutine while the worker
// drains it.
type scanCounter struct {
	io.ReadCloser
	n *atomic.Int64
}

func (c *scanCounter) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// Run drains created events from sub and enqueues a virus_scan job per object.
func (w *Worker) Run(ctx context.Context, sub <-chan repository.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-sub:
			if !ok {
				return
			}
			if e.Type != repository.EventCreated || e.ObjectID == nil {
				continue
			}
			job := repository.Job{
				TenantID:  e.TenantID,
				Type:      JobScan,
				Payload:   EncodeObjectID(*e.ObjectID),
				DedupeKey: fmt.Sprintf("%s:%d", JobScan, *e.ObjectID),
			}
			if _, _, err := w.queue.Enqueue(ctx, job); err != nil {
				w.logger.Warn("antivirus: enqueue scan", "object_id", *e.ObjectID, "err", err)
			}
		}
	}
}

// ScanObjectByID fetches an object, scans it, records the verdict as tags, and
// (when quarantine is enabled) soft-deletes infected objects. Used as the
// virus_scan job handler. Every controller call runs under a pinned
// antivirus-system principal (FR-4), so the audit actor is always
// system:antivirus regardless of the caller context.
func (w *Worker) ScanObjectByID(ctx context.Context, objectID int64) error {
	if w.objects == nil {
		return errors.New("antivirus: object controller is required")
	}
	obj, err := w.repo.GetObjectByID(ctx, objectID)
	if err != nil {
		return fmt.Errorf("get object %d: %w", objectID, err)
	}
	// Tenant-consistency guard: GetObjectByID is unscoped and the job handler
	// runs with the system bypass, so a mismatch between the job tenant and
	// the object's tenant means a job-provenance bug — fail closed instead of
	// tagging/quarantining a foreign tenant's object. The event bridge always
	// copies e.TenantID into the job, so today this is defense in depth.
	if principal, ok := access.PrincipalFrom(ctx); ok &&
		principal.TenantID != "" && principal.TenantID != "*" && principal.TenantID != obj.TenantID {
		return fmt.Errorf("antivirus: job tenant %q does not match object tenant %q", principal.TenantID, obj.TenantID)
	}
	rc, _, err := w.store.Get(ctx, obj.StorageKey)
	if err != nil {
		return fmt.Errorf("storage get %q: %w", obj.StorageKey, err)
	}
	// The HTTP scanner streams the object as the in-flight POST body; count
	// every byte pulled so a remote engine that answers before receiving the
	// whole object is detectable (a client-side lower bound on what it saw).
	// The signature scanner consumes the whole stream inside Scan and is not
	// wrapped — zero overhead on the built-in path. Atomic: after Do returns
	// the transport may keep reading the body from a background goroutine
	// (e.g. when the body cannot be closed), racing with the worker's drain.
	var scanned atomic.Int64
	if _, ok := w.scanner.(*HTTPScanner); ok {
		rc = &scanCounter{ReadCloser: rc, n: &scanned}
	}
	defer rc.Close()

	res, err := w.scanner.Scan(ctx, rc)
	if err != nil {
		return fmt.Errorf("scan %q: %w", obj.Key, err)
	}
	// Drain only for the HTTP scanner: the object stream is the in-flight POST
	// body, and consuming the remainder lets the transport finish/clean up the
	// request (client-side hygiene; the remote service decides how much it
	// reads). The signature scanner consumes the whole stream inside Scan, so
	// draining here would re-read the object for nothing. Any future scanner
	// that returns before consuming its stream must be added to this set.
	if _, ok := w.scanner.(*HTTPScanner); ok {
		_, _ = io.Copy(io.Discard, rc)
		// A remote engine may answer before consuming the request body; the
		// transport then stops reading and closes a closable body early, so
		// fewer bytes than the object size reached it. The verdict may cover
		// only a prefix: surface it as a warning rather than silently tagging
		// the object clean (the truncation fix is client-side; this is the
		// operator-visible signal for the remote-side residual).
		if scanned.Load() < obj.Size {
			w.logger.Warn("antivirus: remote scanner responded before receiving the full object",
				"tenant", obj.TenantID, "key", obj.Key, "scanned_bytes", scanned.Load(), "object_bytes", obj.Size)
		}
	}

	signature := res.Signature
	if len(signature) > maxSignatureBytes {
		signature = signature[:maxSignatureBytes]
	}
	tags := map[string]string{}
	for k, v := range obj.Tags {
		tags[k] = v
	}
	if res.Clean {
		tags[TagStatus] = "clean"
		delete(tags, TagSignature)
	} else {
		tags[TagStatus] = "infected"
		tags[TagSignature] = signature
	}
	controllerCtx := access.WithPrincipal(ctx, access.Principal{
		SubjectID: SystemActor,
		TenantID:  obj.TenantID,
		Kind:      access.PrincipalSystem,
	})
	if err := w.objects.SetObjectTagsByID(controllerCtx, objectID, tags); err != nil {
		return fmt.Errorf("tag object %d: %w", objectID, err)
	}

	if !res.Clean {
		w.logger.Warn("antivirus: infected object", "tenant", obj.TenantID, "key", obj.Key, "signature", signature, "quarantined", w.quarantine)
		if w.quarantine {
			if err := w.objects.QuarantineObjectByID(controllerCtx, objectID, signature); err != nil {
				return fmt.Errorf("quarantine %q: %w", obj.Key, err)
			}
		}
	} else {
		w.logger.Info("antivirus: clean", "tenant", obj.TenantID, "key", obj.Key, "scanner", w.scanner.Name())
	}
	return nil
}
