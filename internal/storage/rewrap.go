package storage

import (
	"context"
	"errors"
	"os"
)

// Rewrapper re-wraps an object's SSE data key to the provider's current master
// key when it was wrapped under an older key version. Implemented by backends
// that perform envelope SSE (currently the local backend). Re-wrapping rewrites
// only the sidecar envelope (kek + kid); the object body is never touched.
type Rewrapper interface {
	// RewrapObject re-wraps key's data key to the current master key. Returns true
	// when a re-wrap occurred — false when SSE is off, the object is plaintext, or
	// it already uses the current key.
	RewrapObject(ctx context.Context, key string) (bool, error)
}

// RewrapReport summarizes a RewrapStale sweep.
type RewrapReport struct {
	Scanned   int
	Rewrapped int
	Failed    int
}

// RewrapStale walks every object in store and re-wraps any whose SSE envelope uses
// an older master-key version, bringing them onto the current key so retired key
// versions can safely be removed from the ring. It is a no-op when store does not
// implement Rewrapper (SSE disabled, or a cloud backend whose provider manages its
// own keys). Idempotent — objects already on the current key are skipped.
func RewrapStale(ctx context.Context, store Storage) (RewrapReport, error) {
	var rep RewrapReport
	rw, ok := store.(Rewrapper)
	if !ok {
		return rep, nil
	}
	marker := ""
	for {
		page, err := store.List(ctx, "", marker, 500)
		if err != nil {
			return rep, err
		}
		for _, obj := range page.Objects {
			rep.Scanned++
			done, err := rw.RewrapObject(ctx, obj.Key)
			if err != nil {
				rep.Failed++
				continue
			}
			if done {
				rep.Rewrapped++
			}
		}
		if !page.HasMore {
			break
		}
		marker = page.NextMarker
	}
	return rep, nil
}

// RewrapObject re-wraps a single object's SSE data key to the current master key,
// rewriting only the sidecar envelope. See Rewrapper.
func (s *LocalStorage) RewrapObject(ctx context.Context, key string) (bool, error) {
	s.generationMu.Lock()
	defer s.generationMu.Unlock()
	if s.enc == nil {
		return false, nil // SSE disabled
	}
	path, err := s.objectPath(key)
	if err != nil {
		return false, err
	}
	meta, err := readMeta(s.metaPath(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, ErrNotFound
		}
		return false, err
	}
	if meta.Envelope == "" {
		return false, nil // plaintext object — nothing to re-wrap
	}
	newEnv, changed, err := s.enc.rewrap(meta.Envelope)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	meta.Envelope = newEnv
	if err := writeMeta(s.metaPath(path), meta); err != nil {
		return false, err
	}
	return true, nil
}
