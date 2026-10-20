package s3compat

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type decimalRangeObject struct {
	endpoint string
	body     string
	version  string
}

func newDecimalRangeObjects(t *testing.T) []decimalRangeObject {
	t.Helper()
	server := newTestServer(t)
	base := server.URL + "/bucket"
	do(t, http.MethodPut, base, nil, nil)
	versioning, _ := xml.Marshal(versioningConfiguration{Status: "Enabled"})
	do(t, http.MethodPut, base+"?versioning", versioning, nil)
	do(t, http.MethodPut, base+"/key.txt", []byte("abcdef"), nil)
	do(t, http.MethodPut, base+"/key.txt", []byte("current"), nil)
	response, body := do(t, http.MethodGet, base+"?versions", nil, nil)
	var versions listVersionsResult
	if err := xml.Unmarshal(body, &versions); err != nil || response.StatusCode != http.StatusOK || len(versions.Versions) != 2 {
		t.Fatalf("version fixture failed: response=%v body=%q err=%v", response, body, err)
	}
	older := versions.Versions[1].VersionID
	return []decimalRangeObject{
		{base + "/key.txt", "current", ""},
		{base + "/key.txt?versionId=" + url.QueryEscape(older), "abcdef", older},
	}
}

func TestRangeDecimalS3MalformedSignsAreIgnored(t *testing.T) {
	for _, selected := range newDecimalRangeObjects(t) {
		for _, value := range []string{"bytes=0--1", "bytes=+1-3", "bytes=1-+3", "bytes=-+3"} {
			response, body := do(t, http.MethodGet, selected.endpoint, nil, map[string]string{"Range": value})
			if response.StatusCode != http.StatusOK || string(body) != selected.body || response.Header.Get("Content-Range") != "" {
				t.Errorf("malformed range sliced S3 content: range=%q code=%d body=%q", value, response.StatusCode, body)
			}
		}
	}
}

func TestRangeDecimalS3OversizedValues(t *testing.T) {
	for _, selected := range newDecimalRangeObjects(t) {
		for _, check := range []struct {
			header string
			offset int
		}{
			{"bytes=1-9223372036854775808", 1},
			{"bytes=-" + strings.Repeat("9", 128), 0},
		} {
			response, body := do(t, http.MethodGet, selected.endpoint, nil, map[string]string{"Range": check.header})
			wantRange := fmt.Sprintf("bytes %d-%d/%d", check.offset, len(selected.body)-1, len(selected.body))
			if response.StatusCode != http.StatusPartialContent || string(body) != selected.body[check.offset:] || response.Header.Get("Content-Range") != wantRange {
				t.Errorf("large range lost S3 span: range=%q code=%d body=%q headers=%v", check.header, response.StatusCode, body, response.Header)
			}
			if selected.version != "" && response.Header.Get("x-amz-version-id") != selected.version {
				t.Errorf("large range lost version pin: %v", response.Header)
			}
		}
	}
}

func TestRangeDecimalS3OversizedStartRemains416(t *testing.T) {
	for _, selected := range newDecimalRangeObjects(t) {
		response, _ := do(t, http.MethodGet, selected.endpoint, nil, map[string]string{"Range": "bytes=9223372036854775808-"})
		if response.StatusCode != http.StatusRequestedRangeNotSatisfiable || response.Header.Get("Content-Range") != fmt.Sprintf("bytes */%d", len(selected.body)) {
			t.Errorf("oversized S3 start returned content: code=%d headers=%v", response.StatusCode, response.Header)
		}
	}
}
