package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// TestValidateContentEncoding keeps the system header bounded without
// normalizing the declaration used by raw reads or the thumbnail gate.
func TestValidateContentEncoding(t *testing.T) {
	for _, value := range []string{" ", "identity", "gzip", "gzip, br", "GZip,br"} {
		if err := ValidateContentEncoding(value); err != nil {
			t.Errorf("safe Content-Encoding %q: %v", value, err)
		}
	}
	for _, value := range []string{"gzip\r\nbr", strings.Repeat("x", MaxContentEncodingLen+1)} {
		err := ValidateContentEncoding(value)
		if err == nil || !errors.Is(err, ErrInvalidArgs) {
			t.Errorf("unsafe Content-Encoding %q: err=%v, want ErrInvalidArgs", value, err)
		}
	}
	if err := ValidateContentEncoding(""); err != nil {
		t.Fatalf("empty Content-Encoding: %v", err)
	}
}

func TestContentEncodingBoundRunsBeforePut(t *testing.T) {
	svc, repo := newTestSvc(t)
	_, err := svc.Put(context.Background(), "", "", "too-long", strings.NewReader("x"), 1, PutOptions{
		ContentEncoding: strings.Repeat("x", MaxContentEncodingLen+1),
	})
	if err == nil || !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("long Content-Encoding error=%v, want ErrInvalidArgs", err)
	}
	if _, err := repo.GetObject(context.Background(), "default", "default", "too-long"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("rejected object lookup error=%v, want not found", err)
	}
}
