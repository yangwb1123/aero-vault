package rest

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

func TestThumbnailRepresentationValidatorRollsForward(t *testing.T) {
	srv, _, svc, _ := newThumbnailCacheREST(t, true, 0)
	base := srv.URL + "/v1/files/representation.png"
	if resp, _ := req(t, "PUT", base, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status=%d, want 201", resp.StatusCode)
	}
	obj, err := svc.Stat(context.Background(), "default", "default", "representation.png")
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	width, height := thumbnail.EffectiveDims(32, 32)
	old := quotedThumbETag(thumbValidatorETagWithRepresentation(thumbnail.CacheKeyVersion,
		"representation-old", thumbnailSourceIdentity(obj), obj.ETag, width, height))
	thumb := base + "/thumbnail?w=32&h=32"
	resp, body := req(t, "GET", thumb, nil, map[string]string{"If-None-Match": old})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("old representation validator status=%d, want 200", resp.StatusCode)
	}
	current := resp.Header.Get("ETag")
	if current == "" || current == old {
		t.Fatalf("rollover ETag=%q, want a new validator", current)
	}
	if len(body) == 0 {
		t.Fatal("rollover 200 returned an empty thumbnail")
	}
	resp, _ = req(t, "GET", thumb, nil, map[string]string{"If-None-Match": current})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("current representation validator status=%d, want 304", resp.StatusCode)
	}
}

func TestThumbValidatorETagRepresentationAxis(t *testing.T) {
	identity := thumbnail.SourceIdentity{TenantID: "tenant", Bucket: "bucket", Key: "key", VersionID: "version"}
	old := thumbValidatorETagWithRepresentation(thumbnail.CacheKeyVersion, "representation-old", identity, "source", 10, 20)
	current := thumbValidatorETag(thumbnail.CacheKeyVersion, identity, "source", 10, 20)
	if old == current {
		t.Fatal("representation changes must change the thumbnail validator")
	}
	prefix := fmt.Sprintf("av-thumb-v%d-", thumbnail.CacheKeyVersion)
	if !strings.HasPrefix(current, prefix) {
		t.Fatalf("validator=%q, want prefix %q", current, prefix)
	}
	if !strings.HasSuffix(current, "-10x20") {
		t.Fatalf("validator=%q, want effective-dimension suffix", current)
	}
}
