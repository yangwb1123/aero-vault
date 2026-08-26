package thumbnail

import (
	"strings"
	"testing"
)

func alternateJPEGQuality() int {
	if quality == 100 {
		return quality - 1
	}
	return quality + 1
}

func currentThumbnailCacheKey(identity SourceIdentity, sourceETag string, effW, effH int) CacheKey {
	return CacheKey{
		Identity: identity, SourceETag: sourceETag,
		EffW: effW, EffH: effH, Version: CacheKeyVersion,
		Representation: currentRepresentationToken,
	}
}

func TestRepresentationTokenTracksJPEGQuality(t *testing.T) {
	if currentRepresentationToken != representationTokenForJPEGQuality(quality) {
		t.Fatal("current representation token is not derived from the encoder quality")
	}
	if currentRepresentationToken == representationTokenForJPEGQuality(alternateJPEGQuality()) {
		t.Fatal("changing JPEG quality must change the representation token")
	}
}

func TestDerivedValidatorTokenTracksRepresentation(t *testing.T) {
	identity := SourceIdentity{TenantID: "tenant", Bucket: "bucket", Key: "key", VersionID: "version"}
	current := DerivedValidatorToken(CacheKeyVersion, identity, "source", 32, 64)
	explicitCurrent := DerivedValidatorTokenWithRepresentation(CacheKeyVersion, currentRepresentationToken, identity, "source", 32, 64)
	alternate := DerivedValidatorTokenWithRepresentation(CacheKeyVersion,
		representationTokenForJPEGQuality(alternateJPEGQuality()), identity, "source", 32, 64)
	if current != explicitCurrent {
		t.Fatalf("implicit current token=%q, explicit current token=%q", current, explicitCurrent)
	}
	if current == alternate {
		t.Fatal("derived validator token must change with the representation")
	}
	if len(current) != 64 || strings.Contains(current, currentRepresentationToken) {
		t.Fatalf("representation token leaked into validator: %q", current)
	}
}
