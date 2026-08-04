package s3compat

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"testing"
)

func TestVersionGetHonorsRangeAndConditions(t *testing.T) {
	server := newTestServer(t)
	base := server.URL
	do(t, http.MethodPut, base+"/bucket", nil, nil)
	versioningBody, _ := xml.Marshal(versioningConfiguration{Status: "Enabled"})
	do(t, http.MethodPut, base+"/bucket?versioning", versioningBody, nil)
	do(t, http.MethodPut, base+"/bucket/key.txt", []byte("abcdef"), nil)
	do(t, http.MethodPut, base+"/bucket/key.txt", []byte("new"), nil)

	response, body := do(t, http.MethodGet, base+"/bucket?versions", nil, nil)
	var versions listVersionsResult
	if err := xml.Unmarshal(body, &versions); err != nil {
		t.Fatalf("decode versions: %v body=%s", err, body)
	}
	if response.StatusCode != http.StatusOK || len(versions.Versions) != 2 {
		t.Fatalf("versions status=%d result=%+v body=%s", response.StatusCode, versions, body)
	}
	old := versions.Versions[1]
	versionURL := base + "/bucket/key.txt?versionId=" + url.QueryEscape(old.VersionID)

	response, body = do(t, http.MethodGet, versionURL, nil, map[string]string{"Range": "bytes=1-3"})
	if response.StatusCode != http.StatusPartialContent || string(body) != "bcd" {
		t.Fatalf("version range status=%d body=%q", response.StatusCode, body)
	}
	if response.Header.Get("Content-Range") != "bytes 1-3/6" ||
		response.Header.Get("x-amz-version-id") != old.VersionID {
		t.Fatalf("version range headers=%v", response.Header)
	}

	response, body = do(t, http.MethodGet, versionURL, nil, map[string]string{
		"If-None-Match": old.ETag,
	})
	if response.StatusCode != http.StatusNotModified || len(body) != 0 {
		t.Fatalf("conditional version GET status=%d body=%q", response.StatusCode, body)
	}
	response, _ = do(t, http.MethodHead, versionURL, nil, map[string]string{
		"If-Match": `"does-not-match"`,
	})
	if response.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("conditional version HEAD status=%d", response.StatusCode)
	}
}
