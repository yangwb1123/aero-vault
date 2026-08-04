package service

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestReservedMetadataNamespaceCannotBeWrittenByCallers(t *testing.T) {
	svc, _ := newTestSvc(t)
	ctx := context.Background()
	reserved := map[string]string{"_AeRo_scrub_status": "corrupt"}

	if _, err := svc.Put(
		ctx, "", "", "reserved.txt", strings.NewReader("body"), 4,
		PutOptions{Metadata: reserved},
	); !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("put reserved metadata error = %v, want ErrInvalidArgs", err)
	}
	if _, err := svc.InitMultipart(
		ctx, "", "", "reserved-multipart.txt", PutOptions{Metadata: reserved},
	); !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("multipart reserved metadata error = %v, want ErrInvalidArgs", err)
	}
}

func TestMetadataMutationsPreserveSystemMetadata(t *testing.T) {
	svc, repo := newTestSvc(t)
	ctx := context.Background()
	body := "body"
	digest := md5.Sum([]byte(body))
	if _, err := svc.Put(
		ctx, "", "", "metadata.txt", strings.NewReader(body), int64(len(body)),
		PutOptions{
			ContentDisposition: "attachment",
			ContentEncoding:    "identity",
			ContentMD5:         base64.StdEncoding.EncodeToString(digest[:]),
			Metadata:           map[string]string{"author": "Ada"},
		},
	); err != nil {
		t.Fatalf("put: %v", err)
	}

	if err := svc.PutMetadata(
		ctx, "", "", "metadata.txt", map[string]string{"title": "Report"},
	); err != nil {
		t.Fatalf("replace metadata: %v", err)
	}
	assertSystemMetadataPreserved(t, repo, "metadata.txt")
	meta, err := svc.GetMetadata(ctx, "", "", "metadata.txt")
	if err != nil {
		t.Fatalf("get metadata: %v", err)
	}
	if len(meta) != 1 || meta["title"] != "Report" {
		t.Fatalf("user metadata = %#v, want title only", meta)
	}

	if err := svc.PatchMetadata(
		ctx, "", "", "metadata.txt", map[string]string{"_aero_scrub_status": "healthy"},
	); !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("patch reserved metadata error = %v, want ErrInvalidArgs", err)
	}
	if err := svc.DeleteMetadataKey(
		ctx, "", "", "metadata.txt", "_aero_content_md5",
	); !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("delete reserved metadata error = %v, want ErrInvalidArgs", err)
	}
	if err := svc.DeleteMetadata(ctx, "", "", "metadata.txt"); err != nil {
		t.Fatalf("delete metadata: %v", err)
	}
	assertSystemMetadataPreserved(t, repo, "metadata.txt")
	meta, err = svc.GetMetadata(ctx, "", "", "metadata.txt")
	if err != nil {
		t.Fatalf("get cleared metadata: %v", err)
	}
	if len(meta) != 0 {
		t.Fatalf("cleared user metadata = %#v, want empty", meta)
	}
}

func assertSystemMetadataPreserved(
	t *testing.T, repo repository.Repository, key string,
) {
	t.Helper()
	obj, err := repo.GetObject(context.Background(), "default", "default", key)
	if err != nil {
		t.Fatalf("get raw object: %v", err)
	}
	for _, metaKey := range []string{
		"_aero_content_disposition", "_aero_content_encoding", "_aero_content_md5",
	} {
		if obj.Metadata[metaKey] == "" {
			t.Fatalf("system metadata %q was removed: %#v", metaKey, obj.Metadata)
		}
	}
}
