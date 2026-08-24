package thumbnail

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSourceIdentityTokenInjective(t *testing.T) {
	base := SourceIdentity{TenantID: "tenant", Bucket: "bucket", Key: "dir/file", VersionID: "version"}
	cases := []struct {
		name string
		mut  func(SourceIdentity) SourceIdentity
	}{
		{"tenant", func(id SourceIdentity) SourceIdentity { id.TenantID = "other"; return id }},
		{"bucket", func(id SourceIdentity) SourceIdentity { id.Bucket = "other"; return id }},
		{"key", func(id SourceIdentity) SourceIdentity { id.Key = "other"; return id }},
		{"version", func(id SourceIdentity) SourceIdentity { id.VersionID = "other"; return id }},
	}
	seen := map[string]string{base.Token(): "base"}
	for _, tc := range cases {
		token := tc.mut(base).Token()
		if prior, ok := seen[token]; ok {
			t.Fatalf("%s token aliases %s: %q", tc.name, prior, token)
		}
		seen[token] = tc.name
	}
	// Length-prefixing must also distinguish adjacent-field boundary cases.
	left := SourceIdentity{TenantID: "ab", Bucket: "c", Key: "key", VersionID: "v"}
	right := SourceIdentity{TenantID: "a", Bucket: "bc", Key: "key", VersionID: "v"}
	if left.Token() == right.Token() {
		t.Fatal("length-prefixed identity token aliases field boundaries")
	}
}

func TestDerivedValidatorTokenIsOpaqueAndVersioned(t *testing.T) {
	identity := SourceIdentity{TenantID: "tenant", Bucket: "bucket", Key: "key", VersionID: "version"}
	raw := `W/"provider,etag"\x00`
	first := DerivedValidatorToken(CacheKeyVersion, identity, raw, 32, 64)
	second := DerivedValidatorToken(CacheKeyVersion+1, identity, raw, 32, 64)
	if len(first) != 64 || first == second {
		t.Fatalf("validator token = %q, next-version token = %q", first, second)
	}
	if strings.Contains(first, raw) || strings.Contains(first, `provider,etag`) {
		t.Fatalf("validator token contains raw ETag material: %q", first)
	}
	for _, value := range []string{identity.TenantID, identity.Bucket, identity.Key, identity.VersionID} {
		if value != "" && strings.Contains(first, value) {
			t.Fatalf("validator token contains raw identity field %q: %q", value, first)
		}
	}
}

func TestCachedGenerationIncompleteIdentityBypassesLookupAndStore(t *testing.T) {
	data := makePNG(t, 32, 32)
	complete := SourceIdentity{TenantID: "tenant", Bucket: "bucket", Key: "key", VersionID: "version"}
	fields := []struct {
		name string
		mut  func(SourceIdentity) SourceIdentity
	}{
		{"tenant", func(id SourceIdentity) SourceIdentity { id.TenantID = ""; return id }},
		{"bucket", func(id SourceIdentity) SourceIdentity { id.Bucket = ""; return id }},
		{"key", func(id SourceIdentity) SourceIdentity { id.Key = ""; return id }},
		{"version", func(id SourceIdentity) SourceIdentity { id.VersionID = ""; return id }},
	}
	for _, tc := range fields {
		t.Run(tc.name, func(t *testing.T) {
			cache := NewCache(1<<20, 0)
			var warmOpens atomic.Int64
			if _, hit, err := GenerateContextWithOpenerCached(
				context.Background(), cache, complete, etagA, 32, 32,
				countingOpener3(data, etagA, &warmOpens, complete),
			); err != nil || hit {
				t.Fatalf("warm generation: err=%v hit=%v", err, hit)
			}
			beforeHits, beforeMisses, beforeEvictions, beforeExpired := cache.Stats()
			beforeLen, beforeBytes := cache.Len(), cache.Bytes()
			var opens atomic.Int64
			incomplete := tc.mut(complete)
			got, hit, err := GenerateContextWithOpenerCached(
				context.Background(), cache, incomplete, etagA, 32, 32,
				countingOpener3(data, etagA, &opens, complete),
			)
			if err != nil || hit || len(got) == 0 {
				t.Fatalf("incomplete generation: err=%v hit=%v bytes=%d", err, hit, len(got))
			}
			if opens.Load() != 1 {
				t.Fatalf("incomplete identity opener calls=%d, want 1", opens.Load())
			}
			afterHits, afterMisses, afterEvictions, afterExpired := cache.Stats()
			if afterHits != beforeHits || afterMisses != beforeMisses ||
				afterEvictions != beforeEvictions || afterExpired != beforeExpired {
				t.Fatalf("incomplete identity changed cache stats: got=%d/%d/%d/%d want=%d/%d/%d/%d",
					afterHits, afterMisses, afterEvictions, afterExpired,
					beforeHits, beforeMisses, beforeEvictions, beforeExpired)
			}
			if cache.Len() != beforeLen || cache.Bytes() != beforeBytes {
				t.Fatalf("incomplete identity changed cache storage: len=%d/%d bytes=%d/%d", cache.Len(), beforeLen, cache.Bytes(), beforeBytes)
			}
		})
	}
}

func TestCachedGenerationRequiresOpenedIdentityAndProof(t *testing.T) {
	data := makePNG(t, 32, 32)
	requested := SourceIdentity{TenantID: "tenant", Bucket: "bucket", Key: "key", VersionID: "version"}
	cases := []struct {
		name   string
		opened OpenedSource
	}{
		{"tenant-mismatch", OpenedSource{Identity: SourceIdentity{TenantID: "other", Bucket: "bucket", Key: "key", VersionID: "version"}, ETag: etagA, Bound: true}},
		{"bucket-mismatch", OpenedSource{Identity: SourceIdentity{TenantID: "tenant", Bucket: "other", Key: "key", VersionID: "version"}, ETag: etagA, Bound: true}},
		{"key-mismatch", OpenedSource{Identity: SourceIdentity{TenantID: "tenant", Bucket: "bucket", Key: "other", VersionID: "version"}, ETag: etagA, Bound: true}},
		{"version-mismatch", OpenedSource{Identity: SourceIdentity{TenantID: "tenant", Bucket: "bucket", Key: "key", VersionID: "other"}, ETag: etagA, Bound: true}},
		{"unbound", OpenedSource{Identity: requested, ETag: etagA, Bound: false}},
		{"incomplete-opened", OpenedSource{Identity: SourceIdentity{TenantID: "tenant", Bucket: "bucket", Key: "key"}, ETag: etagA, Bound: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cache := NewCache(1<<20, 0)
			var opens atomic.Int64
			open := func() (io.ReadCloser, OpenedSource, error) {
				opens.Add(1)
				return io.NopCloser(bytes.NewReader(data)), tc.opened, nil
			}
			for i := 0; i < 2; i++ {
				got, hit, err := GenerateContextWithOpenerCached(
					context.Background(), cache, requested, etagA, 32, 32, open,
				)
				if err != nil || hit || len(got) == 0 {
					t.Fatalf("generation %d: err=%v hit=%v bytes=%d", i+1, err, hit, len(got))
				}
			}
			if opens.Load() != 2 {
				t.Fatalf("proof failure opener calls=%d, want 2", opens.Load())
			}
			if cache.Len() != 0 {
				t.Fatalf("proof failure stored %d cache entries", cache.Len())
			}
		})
	}
}
