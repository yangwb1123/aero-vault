package s3compat

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"testing"
)

func TestListObjectsV2StartAfterAndOpaqueContinuation(t *testing.T) {
	server := newTestServer(t)
	base := server.URL
	for _, key := range []string{"a.txt", "b.txt", "c.txt"} {
		response, body := do(t, http.MethodPut, base+"/bucket/"+key, []byte(key), nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("put %s status=%d body=%s", key, response.StatusCode, body)
		}
	}

	response, body := do(
		t, http.MethodGet,
		base+"/bucket?list-type=2&max-keys=1&start-after=a.txt", nil, nil,
	)
	var first listBucketResult
	if err := xml.Unmarshal(body, &first); err != nil {
		t.Fatalf("decode first page: %v body=%s", err, body)
	}
	if response.StatusCode != http.StatusOK || len(first.Contents) != 1 ||
		first.Contents[0].Key != "b.txt" || !first.IsTruncated {
		t.Fatalf("first page = %+v status=%d body=%s", first, response.StatusCode, body)
	}
	if first.NextContinuationToken == "" || first.NextContinuationToken == "b.txt" {
		t.Fatalf("continuation token is not opaque: %q", first.NextContinuationToken)
	}

	response, body = do(
		t, http.MethodGet,
		base+"/bucket?list-type=2&max-keys=1&continuation-token="+
			url.QueryEscape(first.NextContinuationToken),
		nil, nil,
	)
	var second listBucketResult
	if err := xml.Unmarshal(body, &second); err != nil {
		t.Fatalf("decode second page: %v body=%s", err, body)
	}
	if response.StatusCode != http.StatusOK || len(second.Contents) != 1 ||
		second.Contents[0].Key != "c.txt" || second.IsTruncated {
		t.Fatalf("second page = %+v status=%d body=%s", second, response.StatusCode, body)
	}
}

func TestListObjectsHonorsZeroMaxKeys(t *testing.T) {
	server := newTestServer(t)
	base := server.URL
	do(t, http.MethodPut, base+"/bucket/object.txt", []byte("x"), nil)

	for _, query := range []string{"?list-type=2&max-keys=0", "?max-keys=0"} {
		response, body := do(t, http.MethodGet, base+"/bucket"+query, nil, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("list %q status=%d body=%s", query, response.StatusCode, body)
		}
		var result listBucketResult
		if err := xml.Unmarshal(body, &result); err != nil {
			t.Fatalf("decode list %q: %v body=%s", query, err, body)
		}
		if len(result.Contents) != 0 || result.IsTruncated {
			t.Fatalf("list %q returned objects with max-keys=0: %s", query, body)
		}
	}
}
