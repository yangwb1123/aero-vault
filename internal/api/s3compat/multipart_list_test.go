package s3compat

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"testing"
)

func TestListMultipartUploadsFiltersPrefixAndPaginatesMatches(t *testing.T) {
	server := newTestServer(t)
	base := server.URL
	for _, key := range []string{"a.bin", "logs/one.bin", "logs/two.bin", "z.bin"} {
		response, body := do(t, http.MethodPost, base+"/bucket/"+key+"?uploads", nil, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("initiate %s status=%d body=%s", key, response.StatusCode, body)
		}
	}

	response, body := do(
		t, http.MethodGet,
		base+"/bucket?uploads&prefix=logs/&max-uploads=1", nil, nil,
	)
	var first listMultipartUploadsResult
	if err := xml.Unmarshal(body, &first); err != nil {
		t.Fatalf("decode first page: %v body=%s", err, body)
	}
	if response.StatusCode != http.StatusOK || len(first.Uploads) != 1 ||
		first.Uploads[0].Key != "logs/one.bin" || !first.IsTruncated {
		t.Fatalf("first upload page = %+v status=%d body=%s", first, response.StatusCode, body)
	}

	nextURL := base + "/bucket?uploads&prefix=logs/&max-uploads=1&key-marker=" +
		url.QueryEscape(first.NextKeyMarker) + "&upload-id-marker=" +
		url.QueryEscape(first.NextUploadIDMarker)
	response, body = do(t, http.MethodGet, nextURL, nil, nil)
	var second listMultipartUploadsResult
	if err := xml.Unmarshal(body, &second); err != nil {
		t.Fatalf("decode second page: %v body=%s", err, body)
	}
	if response.StatusCode != http.StatusOK || len(second.Uploads) != 1 ||
		second.Uploads[0].Key != "logs/two.bin" || second.IsTruncated {
		t.Fatalf("second upload page = %+v status=%d body=%s", second, response.StatusCode, body)
	}
}

func TestListMultipartUploadsHonorsZeroLimit(t *testing.T) {
	server := newTestServer(t)
	base := server.URL
	do(t, http.MethodPost, base+"/bucket/object.bin?uploads", nil, nil)
	response, body := do(
		t, http.MethodGet, base+"/bucket?uploads&max-uploads=0", nil, nil,
	)
	var result listMultipartUploadsResult
	if err := xml.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode result: %v body=%s", err, body)
	}
	if response.StatusCode != http.StatusOK || len(result.Uploads) != 0 || result.IsTruncated {
		t.Fatalf("zero-limit result=%+v status=%d body=%s", result, response.StatusCode, body)
	}
}
