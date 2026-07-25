package storage

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// isInternalTemp reports whether name is one of the backend's transient staging
// files (created by atomic temp+rename writes). They must never surface as objects
// in List. Matched by prefix so legitimate dot-prefixed object keys are unaffected.
func isInternalTemp(name string) bool {
	return strings.HasPrefix(name, ".upload-") ||
		strings.HasPrefix(name, ".assemble-") ||
		strings.HasPrefix(name, ".meta-")
}

func (s *LocalStorage) List(ctx context.Context, prefix, marker string, limit int) (ListResult, error) {
	if limit <= 0 {
		limit = 1000
	}
	matches, err := s.collectObjects(ctx, s.cfg.Root, prefix, marker)
	if err != nil {
		return ListResult{}, err
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Key < matches[j].Key })

	res := ListResult{}
	if len(matches) > limit {
		res.Objects = matches[:limit]
		res.NextMarker = matches[limit-1].Key
		res.HasMore = true
	} else {
		res.Objects = matches
	}
	return res, nil
}

func (s *LocalStorage) collectObjects(ctx context.Context, dir, prefix, marker string) ([]ObjectInfo, error) {
	var matches []ObjectInfo
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == ".multipart" {
				return filepath.SkipDir
			}
			return nil
		}
		if isSkippedFile(path, d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if !matchPrefix(prefix, key) {
			return nil
		}
		if skipMarker(marker, key) {
			return nil
		}
		info, err := s.Stat(ctx, key)
		if err != nil {
			return nil
		}
		matches = append(matches, info)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return matches, nil
}

func isSkippedFile(path, name string) bool {
	return strings.HasSuffix(path, localMetaSuffix) || isInternalTemp(name)
}

func matchPrefix(prefix, key string) bool {
	return prefix == "" || strings.HasPrefix(key, prefix)
}

func skipMarker(marker, key string) bool {
	return marker != "" && key <= marker
}
