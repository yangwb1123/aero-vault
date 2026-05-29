package storage

import (
	"testing"
)

func TestLocalContract(t *testing.T) {
	RunContract(t, func(t *testing.T) Storage {
		dir := t.TempDir()
		s, err := NewLocal(LocalConfig{Root: dir, SignKey: "test-key"})
		if err != nil {
			t.Fatalf("new local: %v", err)
		}
		return s
	})
}
