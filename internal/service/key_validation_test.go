package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestFileServiceRejectsInvalidKeysAcrossObjectOperations(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	invalid := "../escape"
	checks := []struct {
		name string
		call func() error
	}{
		{"get", func() error {
			_, _, err := svc.Get(ctx, "", "", invalid)
			return err
		}},
		{"stat", func() error {
			_, err := svc.Stat(ctx, "", "", invalid)
			return err
		}},
		{"delete", func() error {
			return svc.Delete(ctx, "", "", invalid, false)
		}},
		{"restore", func() error {
			return svc.RestoreObject(ctx, "", "", invalid)
		}},
		{"set tags", func() error {
			return svc.SetTags(ctx, "", "", invalid, map[string]string{"a": "b"})
		}},
		{"list versions", func() error {
			_, err := svc.ListVersions(ctx, "", "", invalid)
			return err
		}},
		{"paged versions", func() error {
			_, err := svc.ListObjectVersionsWithOpts(
				ctx, "", "", invalid, repository.VersionListOpts{},
			)
			return err
		}},
		{"get version", func() error {
			_, _, err := svc.GetVersion(ctx, "", "", invalid, "v1")
			return err
		}},
		{"stat version", func() error {
			_, err := svc.StatVersionWithOptions(ctx, "", "", invalid, "v1", ReadOptions{})
			return err
		}},
		{"range version", func() error {
			_, _, err := svc.GetVersionRangeWithOptions(
				ctx, "", "", invalid, "v1", 0, 1, ReadOptions{},
			)
			return err
		}},
		{"set acl", func() error {
			return svc.SetObjectACL(ctx, "", "", invalid, ACLPrivate)
		}},
		{"get acl", func() error {
			_, err := svc.GetObjectACL(ctx, "", "", invalid)
			return err
		}},
		{"legal hold", func() error {
			return svc.PutLegalHold(ctx, "", "", invalid, "", "", "")
		}},
		{"retention", func() error {
			_, err := svc.SetObjectRetention(
				ctx, "", "", invalid, "", "GOVERNANCE", time.Now().Add(time.Hour),
			)
			return err
		}},
		{"presign get", func() error {
			_, err := svc.PresignGet(ctx, "", "", invalid, time.Minute)
			return err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); !errors.Is(err, ErrInvalidArgs) {
				t.Fatalf("error = %v, want ErrInvalidArgs", err)
			}
		})
	}
	if svc.ObjectPublicReadable(ctx, "", "", invalid) {
		t.Fatal("invalid key must not be publicly readable")
	}
}
