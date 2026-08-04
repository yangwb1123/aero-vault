package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/storage"
)

func TestPutRejectsContentLengthMismatchAndRemovesBlob(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		declared int64
	}{
		{name: "short body", body: "abc", declared: 4},
		{name: "long body", body: "abcdef", declared: 3},
		{name: "nonempty body declared empty", body: "x", declared: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newTestSvc(t)
			_, err := svc.Put(
				context.Background(), "", "", "mismatch.bin",
				strings.NewReader(tc.body), tc.declared, PutOptions{},
			)
			if !errors.Is(err, ErrSizeMismatch) {
				t.Fatalf("put error = %v, want ErrSizeMismatch", err)
			}
			if _, err := svc.Stat(
				context.Background(), "", "", "mismatch.bin",
			); !errors.Is(err, ErrNotFound) {
				t.Fatalf("repository object after mismatch = %v, want ErrNotFound", err)
			}
			if _, err := svc.Storage().Stat(
				context.Background(), "default/default/mismatch.bin",
			); !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("storage object after mismatch = %v, want storage.ErrNotFound", err)
			}
		})
	}
}

func TestPutUnknownSizeUsesMaterializedLength(t *testing.T) {
	svc, _ := newTestSvc(t)
	if _, err := svc.Put(
		context.Background(), "", "", "unknown.bin",
		strings.NewReader("body"), -1, PutOptions{},
	); err != nil {
		t.Fatalf("unknown-size put: %v", err)
	}
	assertObjectBody(t, svc, "unknown.bin", "body")
}
