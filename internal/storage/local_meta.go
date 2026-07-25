package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func (m localMeta) toInfo() ObjectInfo {
	return ObjectInfo{
		Key:          m.Key,
		Size:         m.Size,
		ETag:         m.ETag,
		ContentType:  m.ContentType,
		LastModified: m.LastModified,
		Metadata:     m.Metadata,
	}
}

func writeMeta(path string, m localMeta) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	// Write atomically (temp + rename) so a crash can't leave a torn sidecar —
	// critical for re-wrap, which rewrites the envelope holding the wrapped key.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".meta-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

func readMeta(path string) (localMeta, error) {
	var m localMeta
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	return m, nil
}
