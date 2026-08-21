package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestSetQuotaRejectsNegativeLimits(t *testing.T) {
	ctx := context.Background()
	svc, repo := newTestSvc(t)
	if err := svc.SetQuota(ctx, "default", 100, 2); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		bytes, objects int64
		want           string
	}{
		"bytes":   {-1, 2, "max_bytes"},
		"objects": {100, -1, "max_objects"},
	} {
		t.Run(name, func(t *testing.T) {
			err := svc.SetQuota(ctx, "default", tc.bytes, tc.objects)
			if !errors.Is(err, ErrInvalidArgs) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SetQuota(%d,%d) = %v, want invalid %s", tc.bytes, tc.objects, err, tc.want)
			}
		})
	}
	q, err := repo.GetTenantQuota(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	if q.MaxBytes != 100 || q.MaxObjects != 2 {
		t.Fatalf("invalid quota changed stored limits: %+v", q)
	}
}

func TestBucketQuotaRejectsNegativeLimits(t *testing.T) {
	svc, _ := newTestSvc(t)
	for _, tc := range []struct {
		name           string
		bytes, objects int64
		want           string
	}{
		{"bytes", -1, 2, "max_bytes"},
		{"objects", 100, -1, "max_objects"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.SetBucketQuota(context.Background(), "default", "default", tc.bytes, tc.objects)
			if !errors.Is(err, ErrInvalidArgs) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SetBucketQuota(%d,%d) = %v, want invalid %s", tc.bytes, tc.objects, err, tc.want)
			}
		})
	}
}

func TestLifecycleRejectsNegativeDays(t *testing.T) {
	svc, _ := newTestSvc(t)
	ctx := context.Background()
	cases := []struct {
		name string
		cfg  repository.LifecycleConfig
		want string
	}{
		{"expire", repository.LifecycleConfig{ExpireAfterDays: -1}, "days"},
		{"noncurrent", repository.LifecycleConfig{NoncurrentDays: -1}, "noncurrent_days"},
		{"count", repository.LifecycleConfig{NoncurrentCount: -1}, "noncurrent_count"},
		{"transition", repository.LifecycleConfig{
			TransitionRules: []repository.TransitionRule{{Days: -1}},
		}, "transition_rules[0].days"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.SetBucketLifecycleFull(ctx, "default", "default", tc.cfg)
			if !errors.Is(err, ErrInvalidArgs) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SetBucketLifecycleFull = %v, want invalid %s", err, tc.want)
			}
		})
	}
	if err := svc.SetBucketLifecycle(ctx, "default", "default", -1, "soft_delete"); !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("SetBucketLifecycle(-1) = %v, want ErrInvalidArgs", err)
	}
}
