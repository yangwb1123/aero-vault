package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// Job types emitted by the indexer's event bridge. Handlers for these are
// registered against the jobs.Pool in main; see EncodeObjectID/DecodeObjectID
// for the legacy payload contract.
const (
	JobIndexObject  = "index_object"
	JobDeleteChunks = "delete_chunks"
)

// Enqueuer is the slice of the job queue the indexer needs. Satisfied by
// *jobs.Queue; kept as an interface so the ai package stays free of a jobs
// import.
type Enqueuer interface {
	Enqueue(ctx context.Context, j repository.Job) (int64, bool, error)
}

type objectIDPayload struct {
	ObjectID int64 `json:"object_id"`
}

// EncodeObjectID builds a legacy job payload referencing an object.
func EncodeObjectID(id int64) string {
	b, _ := json.Marshal(objectIDPayload{ObjectID: id})
	return string(b)
}

// DecodeObjectID parses a legacy object-id job payload.
func DecodeObjectID(payload string) (int64, error) {
	var p objectIDPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return 0, fmt.Errorf("decode object_id payload: %w", err)
	}
	if p.ObjectID == 0 {
		return 0, errors.New("payload missing object_id")
	}
	return p.ObjectID, nil
}

// Indexer turns object lifecycle events into searchable chunks.
//
//	created/updated → extract text → chunk → embed → InsertChunks
//	deleted         → delete exact-scope chunks
//
// It is started once per process; events arrive either from the in-process
// event bus (immediate) or from a periodic catch-up poll against the
// repository (handles restarts and dropped subscribers).
type Indexer struct {
	repo      repository.Repository
	store     storage.Storage
	extractor Extractor
	chunker   *Chunker
	embedder  Embedder
	pii       *PIIDetector
	redact    bool
	logger    *slog.Logger
	queue     Enqueuer
	sinks     []ChunkSink
	scoped    []ScopedChunkSink
	sinkFault error
	tagger    ObjectTagger

	pollEvery time.Duration
	batch     int
}

// ObjectTagger is the FileService slice used for exact-version PII tags.
type ObjectTagger interface {
	SetObjectTagsByID(ctx context.Context, objectID int64, tags map[string]string) error
}

// WithQueue makes the indexer a thin event→job bridge: instead of extracting
// and embedding inline, the event consumer enqueues index_object/delete_chunks
// jobs for the worker pool to run (with durable retry). When nil, the indexer
// processes events inline (the original behavior).
func (ix *Indexer) WithQueue(q Enqueuer) *Indexer {
	ix.queue = q
	return ix
}

// WithChunkSink registers a write-through sink that is notified after the
// repository's chunk rows change. May be called multiple times; every valid
// scoped sink receives every upsert/delete. Invalid registrations latch a
// configuration fault checked by ValidateChunkSinks and before protected work.
func (ix *Indexer) WithChunkSink(s ChunkSink) *Indexer {
	ix.sinks = append(ix.sinks, s)
	ix.registerChunkSink(s)
	return ix
}

// WithObjectTagger routes PII tag updates through FileService.
func (ix *Indexer) WithObjectTagger(tagger ObjectTagger) *Indexer {
	ix.tagger = tagger
	return ix
}

// WithPII enables a PII scan + (optional) redaction in front of the chunker.
// When redact is true, the chunked text seen by the embedder is the redacted
// copy. Counts are written to repository as object metadata via tags.
func (ix *Indexer) WithPII(p *PIIDetector, redact bool) *Indexer {
	ix.pii = p
	ix.redact = redact
	return ix
}

func NewIndexer(
	repo repository.Repository,
	store storage.Storage,
	ext Extractor,
	chunker *Chunker,
	emb Embedder,
	logger *slog.Logger,
) *Indexer {
	if logger == nil {
		logger = slog.Default()
	}
	if chunker == nil {
		chunker = NewChunker()
	}
	return &Indexer{
		repo:      repo,
		store:     store,
		extractor: ext,
		chunker:   chunker,
		embedder:  emb,
		logger:    logger,
		pollEvery: 5 * time.Second,
		batch:     32,
	}
}

// Run blocks until ctx is canceled. It drains live events from sub *and* polls
// the durable queue every pollEvery seconds — this makes restarts safe.
func (ix *Indexer) Run(ctx context.Context, sub <-chan repository.Event) {
	tick := time.NewTicker(ix.pollEvery)
	defer tick.Stop()
	for {
		// Always try to flush the backlog first.
		ix.drainBacklog(ctx)
		select {
		case <-ctx.Done():
			return
		case e, ok := <-sub:
			if !ok {
				return
			}
			ix.handle(ctx, e)
		case <-tick.C:
			// loop back into drainBacklog
		}
	}
}

func (ix *Indexer) drainBacklog(ctx context.Context) {
	events, err := ix.repo.NextUnconsumedEvents(ctx, ix.batch)
	if err != nil {
		ix.logger.Warn("indexer poll failed", "err", err)
		return
	}
	for _, e := range events {
		ix.handle(ctx, e)
	}
}

func (ix *Indexer) handle(ctx context.Context, e repository.Event) {
	if err := ix.processEvent(ctx, e); err != nil {
		ix.logger.Warn("indexer: event processing failed", "id", e.ID, "err", err)
		return
	}
	if err := ix.repo.MarkEventConsumed(ctx, e.ID); err != nil {
		ix.logger.Warn("mark consumed", "id", e.ID, "err", err)
	}
}

// ReindexStale re-indexes objects whose chunks were embedded by a model other
// than the current one (e.g. after the embedder was changed), so the corpus
// stays consistent with the active model. Only genuinely indexed objects are
// counted; skips do not inflate the result and any fault is returned.
func (ix *Indexer) ReindexStale(ctx context.Context, tenant string, limit int) (int, error) {
	if ix.embedder == nil {
		return 0, fmt.Errorf("reindex: no embedder configured")
	}
	if err := ix.ValidateChunkSinks(); err != nil {
		return 0, err
	}
	ids, err := ix.repo.ListObjectIDsToReindex(ctx, tenant, ix.embedder.Name(), limit)
	if err != nil {
		return 0, err
	}
	n := 0
	var firstFault error
	for _, id := range ids {
		indexed, err := ix.indexObjectByID(ctx, id, tenant, nil)
		if err != nil {
			ix.logger.Warn("reindex stale object", "object_id", id, "err", err)
			if firstFault == nil {
				firstFault = err
			}
			continue
		}
		if indexed {
			n++
		}
	}
	if n > 0 {
		ix.logger.Info("reindexed stale objects", "count", n, "model", ix.embedder.Name())
	}
	if firstFault != nil {
		return n, fmt.Errorf("reindex stale: %w", firstFault)
	}
	return n, nil
}

// IndexObjectByID extracts text from an object, chunks it, embeds the chunks,
// and replaces the object's stored chunks. It is idempotent (it deletes old
// chunks first) so retries are safe. Unsupported or empty content is a skip
// success — only genuine failures (storage, embed, persistence) return errors,
// which the job queue will retry.
func (ix *Indexer) IndexObjectByID(ctx context.Context, objectID int64) error {
	_, err := ix.indexObjectByID(ctx, objectID, "", nil)
	return err
}

func (ix *Indexer) indexObjectByID(ctx context.Context, objectID int64, tenant string, expected *access.ChunkRef) (bool, error) {
	if err := ix.ValidateChunkSinks(); err != nil {
		return false, err
	}
	if ix.embedder == nil {
		return false, fmt.Errorf("index: no embedder configured")
	}
	if objectID <= 0 {
		return false, fmt.Errorf("index: object ID must be positive")
	}
	obj, err := ix.repo.GetObjectByID(ctx, objectID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return false, ix.cleanupVanishedObject(ctx, objectID, tenant, expected)
		}
		return false, fmt.Errorf("get object %d: %w", objectID, err)
	}
	ref, err := refFromObject(obj)
	if err != nil {
		return false, err
	}
	if tenant != "" && ref.TenantID != tenant {
		return false, refConflict(ref, "object tenant")
	}
	if expected != nil && ref != *expected {
		return false, refConflict(ref, "expected ref")
	}
	current, err := ix.prepareCurrentObject(ctx, obj)
	if err != nil || !current {
		return false, err
	}
	return ix.indexLoadedObject(ctx, obj, ref)
}

func (ix *Indexer) indexLoadedObject(ctx context.Context, obj repository.Object, ref access.ChunkRef) (bool, error) {
	rc, _, err := ix.store.Get(ctx, obj.StorageKey)
	if err != nil {
		if errors.Is(err, storage.ErrSSECustomerKeyRequired) {
			telemetry.IncIndexerSkip(ctx, "unsupported")
			ix.logger.Info("indexer: skipping SSE-C object", "key", obj.Key)
			return false, nil
		}
		return false, fmt.Errorf("storage get %q: %w", obj.StorageKey, err)
	}
	defer rc.Close()

	text, err := ix.extractor.Extract(ctx, obj.ContentType, rc)
	text, err = ix.applyPII(ctx, obj, text, err)
	if err != nil {
		return false, ix.handleExtractError(obj.Key, err)
	}
	pieces := ix.chunker.Chunk(text)
	if len(pieces) == 0 {
		telemetry.IncIndexerSkip(ctx, "empty")
		return false, nil
	}
	vectors, err := ix.embedder.Embed(ctx, pieces)
	if err != nil {
		return false, fmt.Errorf("embed %q: %w", obj.Key, err)
	}
	if err := ix.repo.DeleteChunksForObject(ctx, obj.ID); err != nil {
		ix.logger.Warn("indexer: delete old chunks", "id", obj.ID, "err", err)
	}
	chunks := ix.buildChunks(obj, pieces, vectors)
	if err := ix.repo.InsertChunks(ctx, chunks); err != nil {
		return false, fmt.Errorf("insert chunks %d: %w", obj.ID, err)
	}
	if err := ix.pushToSinks(ctx, ref, chunks); err != nil {
		return false, err
	}
	ix.logger.Info("indexed", "tenant", obj.TenantID, "key", obj.Key, "chunks", len(chunks), "model", ix.embedder.Name())
	return true, nil
}

func (ix *Indexer) applyPII(ctx context.Context, obj repository.Object, text string, err error) (string, error) {
	if err == nil && ix.pii != nil {
		if hits := ix.pii.Scan(text); len(hits) > 0 {
			tags := map[string]string{}
			for k, v := range obj.Tags {
				tags[k] = v
			}
			tags["pii_scan"] = MapPII(hits)
			if ix.tagger == nil {
				ix.logger.Warn("indexer: PII tagger unavailable", "object_id", obj.ID)
			} else if err := ix.tagger.SetObjectTagsByID(ctx, obj.ID, tags); err != nil {
				ix.logger.Warn("indexer: write PII tags", "object_id", obj.ID, "err", err)
			}
		}
		if ix.redact {
			text = ix.pii.Redact(text, nil)
		}
	}
	return text, err
}

func (ix *Indexer) handleExtractError(key string, err error) error {
	if errors.Is(err, ErrUnsupported) {
		ix.logger.Info("indexer: skipping unsupported content", "key", key)
		telemetry.IncIndexerSkip(context.Background(), "unsupported")
		return nil
	}
	telemetry.IncIndexerSkip(context.Background(), "error")
	return fmt.Errorf("extract %q: %w", key, err)
}

func (ix *Indexer) buildChunks(obj repository.Object, pieces []string, vectors [][]float32) []repository.Chunk {
	chunks := make([]repository.Chunk, 0, len(pieces))
	for i, p := range pieces {
		c := repository.Chunk{
			ObjectID:   obj.ID,
			TenantID:   obj.TenantID,
			Bucket:     obj.Bucket,
			ObjectKey:  obj.Key,
			Seq:        i,
			Content:    p,
			EmbedModel: ix.embedder.Name(),
		}
		if i < len(vectors) {
			c.Embedding = vectors[i]
			c.Dim = len(vectors[i])
		}
		chunks = append(chunks, c)
	}
	return chunks
}
