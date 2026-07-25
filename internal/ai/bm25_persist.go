package ai

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/aero-vault/aero-vault/internal/storage"
)

// --------------------------------------------------------------------------
// Persistence: save / load the in-memory BM25 index to/from a storage backend.
// The snapshot format is gzip-compressed JSON prefixed with a magic header
// ("BM25v1\x00") for format detection. See TASK-004 in the expansion-v124
// implementation plan for the full specification.
// --------------------------------------------------------------------------

// bm25BlobKey returns the storage key used to persist a tenant's BM25 state.
// The key starts with a reserved prefix that avoids collision with regular
// object keys.
func bm25BlobKey(tenant string) string {
	return fmt.Sprintf("__bm25/v1/%s", tenant)
}

var (
	// bm25Magic is a 7-byte prefix written before the gzip-compressed JSON
	// snapshot: "BM25v1" plus a zero terminator. Keeping the magic separate
	// from the gzip stream means we can validate it with a simple prefix check
	// and pass the remainder directly to gzip.NewReader.
	bm25Magic = []byte("BM25v1\x00")
	// ErrBM25UnsupportedFormat is returned by Load when the blob does not
	// carry a recognised BM25 snapshot header.
	ErrBM25UnsupportedFormat = errors.New("bm25: unsupported snapshot format")
)

// bm25Snapshot is the on-disk representation of a BM25 index.
type bm25Snapshot struct {
	Version  int               `json:"version"`
	K1       float64           `json:"k1"`
	B        float64           `json:"b"`
	AvgLen   float64           `json:"avg_len"`
	TotalDoc int               `json:"total_doc"`
	TotalLen int               `json:"total_len"`
	Docs     []bm25SnapshotDoc `json:"docs,omitempty"`
	DF       map[string]int    `json:"df,omitempty"`
	ObjDocs  map[int64][]int64 `json:"obj_docs,omitempty"`
}

// bm25SnapshotDoc is the serialisable form of one indexed document.
type bm25SnapshotDoc struct {
	ID        int64          `json:"id"`
	Tenant    string         `json:"tenant"`
	Bucket    string         `json:"bucket"`
	ObjectKey string         `json:"object_key"`
	ObjectID  int64          `json:"object_id"`
	Seq       int            `json:"seq"`
	Content   string         `json:"content"`
	Length    int            `json:"length"`
	Tokens    map[string]int `json:"tokens"`
}

// toSnapshot builds a serialisable snapshot of the current index state.
// Caller must hold at least a read lock.
func (b *BM25) toSnapshot() bm25Snapshot {
	s := bm25Snapshot{
		Version:  1,
		K1:       b.k1,
		B:        b.b,
		AvgLen:   b.avgLen,
		TotalDoc: b.totalDoc,
		TotalLen: b.totalLen,
	}
	if len(b.docs) > 0 {
		s.Docs = make([]bm25SnapshotDoc, 0, len(b.docs))
		for id, d := range b.docs {
			s.Docs = append(s.Docs, bm25SnapshotDoc{
				ID:        id,
				Tenant:    d.tenant,
				Bucket:    d.bucket,
				ObjectKey: d.objectKey,
				ObjectID:  d.objectID,
				Seq:       d.seq,
				Content:   d.content,
				Length:    d.length,
				Tokens:    d.tokens,
			})
		}
	}
	if len(b.df) > 0 {
		s.DF = make(map[string]int, len(b.df))
		for k, v := range b.df {
			s.DF[k] = v
		}
	}
	if len(b.objDocs) > 0 {
		s.ObjDocs = make(map[int64][]int64, len(b.objDocs))
		for k, ids := range b.objDocs {
			idsCopy := make([]int64, len(ids))
			copy(idsCopy, ids)
			s.ObjDocs[k] = idsCopy
		}
	}
	return s
}

// fromSnapshot restores the index from a snapshot. Caller must hold the write
// lock.
func (b *BM25) fromSnapshot(s bm25Snapshot) {
	b.k1 = s.K1
	b.b = s.B
	b.avgLen = s.AvgLen
	b.totalDoc = s.TotalDoc
	b.totalLen = s.TotalLen

	b.docs = make(map[int64]bm25Doc, len(s.Docs))
	for _, sd := range s.Docs {
		b.docs[sd.ID] = bm25Doc{
			tenant:    sd.Tenant,
			bucket:    sd.Bucket,
			objectKey: sd.ObjectKey,
			objectID:  sd.ObjectID,
			seq:       sd.Seq,
			content:   sd.Content,
			length:    sd.Length,
			tokens:    sd.Tokens,
		}
	}

	b.df = make(map[string]int, len(s.DF))
	for k, v := range s.DF {
		b.df[k] = v
	}

	b.objDocs = make(map[int64][]int64, len(s.ObjDocs))
	for k, ids := range s.ObjDocs {
		idsCopy := make([]int64, len(ids))
		copy(idsCopy, ids)
		b.objDocs[k] = idsCopy
	}
}

// marshalBM25 serialises the current state as gzip-compressed JSON prefixed
// with a magic header ("BM25v1\x00"). The caller must hold at least a read
// lock; the serialisation copies all data under the lock so the caller can
// release it before writing to storage.
func marshalBM25(b *BM25) ([]byte, error) {
	snap := b.toSnapshot()
	raw, err := json.Marshal(snap)
	if err != nil {
		return nil, fmt.Errorf("bm25 marshal: %w", err)
	}
	var gzbuf bytes.Buffer
	zw := gzip.NewWriter(&gzbuf)
	if _, err := zw.Write(raw); err != nil {
		return nil, fmt.Errorf("bm25 gzip: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("bm25 gzip close: %w", err)
	}
	var out bytes.Buffer
	out.Write(bm25Magic)
	out.Write(gzbuf.Bytes())
	return out.Bytes(), nil
}

// unmarshalBM25 reads a snapshot produced by marshalBM25. It validates the
// magic header and returns the deserialised snapshot.
func unmarshalBM25(data []byte) (bm25Snapshot, error) {
	if len(data) < len(bm25Magic) || !bytes.Equal(data[:len(bm25Magic)], bm25Magic) {
		return bm25Snapshot{}, ErrBM25UnsupportedFormat
	}
	zr, err := gzip.NewReader(bytes.NewReader(data[len(bm25Magic):]))
	if err != nil {
		return bm25Snapshot{}, fmt.Errorf("bm25 gzip reader: %w", err)
	}
	defer zr.Close()
	decompressed, err := io.ReadAll(zr)
	if err != nil {
		return bm25Snapshot{}, fmt.Errorf("bm25 decompress: %w", err)
	}
	var snap bm25Snapshot
	if err := json.Unmarshal(decompressed, &snap); err != nil {
		return bm25Snapshot{}, fmt.Errorf("bm25 unmarshal: %w", err)
	}
	return snap, nil
}

// Save persists the current BM25 index state to the storage backend under a
// well-known key derived from the tenant. The snapshot is gzip-compressed JSON
// with a magic header. If the index is clean (no modifications since the last
// Save or Load), Save is a no-op.
func (b *BM25) Save(ctx context.Context, st storage.Storage, tenant string) error {
	data, err := func() ([]byte, error) {
		b.mu.RLock()
		defer b.mu.RUnlock()
		if !b.dirty {
			return nil, nil // clean — no write needed
		}
		return marshalBM25(b)
	}()
	if err != nil {
		return err
	}
	if data == nil {
		return nil // clean
	}
	key := bm25BlobKey(tenant)
	if _, err := st.Put(ctx, key, bytes.NewReader(data), int64(len(data)), storage.PutOptions{}); err != nil {
		return fmt.Errorf("bm25 save: %w", err)
	}
	b.mu.Lock()
	b.dirty = false
	b.mu.Unlock()
	return nil
}

// Load restores the BM25 index from a snapshot stored by Save. It returns
// true when a snapshot was found and loaded; false when no snapshot exists
// (callers should fall back to BuildFromRepo). An unparseable snapshot
// returns ErrBM25UnsupportedFormat.
func (b *BM25) Load(ctx context.Context, st storage.Storage, tenant string) (bool, error) {
	rc, _, err := st.Get(ctx, bm25BlobKey(tenant))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("bm25 load read: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return false, fmt.Errorf("bm25 load read body: %w", err)
	}
	snap, err := unmarshalBM25(data)
	if err != nil {
		return false, err
	}
	b.mu.Lock()
	b.fromSnapshot(snap)
	b.dirty = false
	b.mu.Unlock()
	return true, nil
}
