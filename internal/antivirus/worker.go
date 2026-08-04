package antivirus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

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

// Enqueuer is the slice of the job queue the bridge needs.
type Enqueuer interface {
	Enqueue(ctx context.Context, j repository.Job) (int64, bool, error)
}

// ObjectController is the FileService slice needed by antivirus jobs.
type ObjectController interface {
	SetObjectTagsByID(ctx context.Context, objectID int64, tags map[string]string) error
	QuarantineObjectByID(ctx context.Context, objectID int64) error
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
// virus_scan job handler.
func (w *Worker) ScanObjectByID(ctx context.Context, objectID int64) error {
	if w.objects == nil {
		return errors.New("antivirus: object controller is required")
	}
	obj, err := w.repo.GetObjectByID(ctx, objectID)
	if err != nil {
		return fmt.Errorf("get object %d: %w", objectID, err)
	}
	rc, _, err := w.store.Get(ctx, obj.StorageKey)
	if err != nil {
		return fmt.Errorf("storage get %q: %w", obj.StorageKey, err)
	}
	defer rc.Close()

	res, err := w.scanner.Scan(ctx, rc)
	if err != nil {
		return fmt.Errorf("scan %q: %w", obj.Key, err)
	}
	_, _ = io.Copy(io.Discard, rc) // drain remainder

	tags := map[string]string{}
	for k, v := range obj.Tags {
		tags[k] = v
	}
	if res.Clean {
		tags[TagStatus] = "clean"
		delete(tags, TagSignature)
	} else {
		tags[TagStatus] = "infected"
		tags[TagSignature] = res.Signature
	}
	if err := w.objects.SetObjectTagsByID(ctx, objectID, tags); err != nil {
		return fmt.Errorf("tag object %d: %w", objectID, err)
	}

	if !res.Clean {
		w.logger.Warn("antivirus: infected object", "tenant", obj.TenantID, "key", obj.Key, "signature", res.Signature, "quarantined", w.quarantine)
		if w.quarantine {
			if err := w.objects.QuarantineObjectByID(ctx, objectID); err != nil {
				return fmt.Errorf("quarantine %q: %w", obj.Key, err)
			}
		}
	} else {
		w.logger.Info("antivirus: clean", "tenant", obj.TenantID, "key", obj.Key, "scanner", w.scanner.Name())
	}
	return nil
}
