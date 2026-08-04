// Package replication asynchronously copies stored objects to a secondary
// storage backend (a different region or provider) for disaster recovery. It
// runs off the background job queue: a created event enqueues a replicate job
// that streams the object from the primary backend into the replica at the same
// storage key.
package replication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// JobReplicate is the job type for asynchronous replication.
const JobReplicate = "replicate"

// TagStatus records replication state on the object's tags.
const TagStatus = "repl_status"

// Enqueuer is the slice of the job queue the bridge needs.
type Enqueuer interface {
	Enqueue(ctx context.Context, j repository.Job) (int64, bool, error)
}

// ObjectTagger is the FileService slice used for exact-version status tags.
type ObjectTagger interface {
	SetObjectTagsByID(ctx context.Context, objectID int64, tags map[string]string) error
}

type payload struct {
	ObjectID int64 `json:"object_id"`
}

// EncodeObjectID builds a replicate job payload.
func EncodeObjectID(id int64) string {
	b, _ := json.Marshal(payload{ObjectID: id})
	return string(b)
}

// DecodeObjectID parses a replicate job payload.
func DecodeObjectID(p string) (int64, error) {
	var v payload
	if err := json.Unmarshal([]byte(p), &v); err != nil {
		return 0, fmt.Errorf("decode replicate payload: %w", err)
	}
	if v.ObjectID == 0 {
		return 0, errors.New("payload missing object_id")
	}
	return v.ObjectID, nil
}

// Worker bridges created events to replicate jobs and executes them.
type Worker struct {
	repo    repository.Repository
	primary storage.Storage
	replica storage.Storage
	queue   Enqueuer
	tagger  ObjectTagger
	logger  *slog.Logger
}

func NewWorker(repo repository.Repository, primary, replica storage.Storage, queue Enqueuer, logger *slog.Logger) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{repo: repo, primary: primary, replica: replica, queue: queue, logger: logger}
}

// WithObjectTagger routes status mutations through FileService.
func (w *Worker) WithObjectTagger(tagger ObjectTagger) *Worker {
	w.tagger = tagger
	return w
}

// Run drains created events and enqueues a replicate job per object.
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
				Type:      JobReplicate,
				Payload:   EncodeObjectID(*e.ObjectID),
				DedupeKey: fmt.Sprintf("%s:%d", JobReplicate, *e.ObjectID),
			}
			if _, _, err := w.queue.Enqueue(ctx, job); err != nil {
				w.logger.Warn("replication: enqueue", "object_id", *e.ObjectID, "err", err)
			}
		}
	}
}

// ReplicateObjectByID streams an object from the primary backend into the
// replica at the same storage key and records repl_status=replicated. Used as
// the replicate job handler; idempotent (overwrites the replica copy).
func (w *Worker) ReplicateObjectByID(ctx context.Context, objectID int64) error {
	obj, err := w.repo.GetObjectByID(ctx, objectID)
	if err != nil {
		return fmt.Errorf("get object %d: %w", objectID, err)
	}
	if _, _, ok := service.SSECustomerInfo(obj.Metadata); ok {
		return errors.New("replication: SSE-C object requires an unavailable customer key")
	}
	rc, info, err := w.primary.Get(ctx, obj.StorageKey)
	if err != nil {
		return fmt.Errorf("primary get %q: %w", obj.StorageKey, err)
	}
	defer rc.Close()

	putOptions := storage.PutOptions{
		ContentType: obj.ContentType,
		Metadata:    obj.Metadata,
	}
	if algorithm, keyID, ok := service.ServerSideEncryptionInfo(obj.Metadata); ok {
		putOptions.SSEAlgorithm = algorithm
		putOptions.SSEKMSKeyID = keyID
	}
	if _, err := w.replica.Put(ctx, obj.StorageKey, rc, info.Size, putOptions); err != nil {
		return fmt.Errorf("replica put %q: %w", obj.StorageKey, err)
	}

	tags := map[string]string{}
	for k, v := range obj.Tags {
		tags[k] = v
	}
	tags[TagStatus] = "replicated"
	if w.tagger == nil {
		w.logger.Warn("replication: object tagger unavailable", "object_id", objectID)
	} else if err := w.tagger.SetObjectTagsByID(ctx, objectID, tags); err != nil {
		// Replica write already succeeded; a tag failure shouldn't fail the job.
		w.logger.Warn("replication: tag update", "object_id", objectID, "err", err)
	}
	w.logger.Info("replicated", "tenant", obj.TenantID, "key", obj.Key, "backend", w.replica.Backend())
	return nil
}
