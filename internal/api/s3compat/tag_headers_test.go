package s3compat

import (
	"encoding/xml"
	"net/http"
	"testing"
)

func TestPutAndMultipartTaggingHeaders(t *testing.T) {
	server := newTestServer(t)
	putURL := server.URL + "/bucket/tagged.txt"
	resp, body := do(t, http.MethodPut, putURL, []byte("tagged"), map[string]string{
		"x-amz-tagging": "env=prod&team=core",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tagged PUT status = %d, body=%s", resp.StatusCode, body)
	}
	assertObjectTags(t, putURL, map[string]string{"env": "prod", "team": "core"})

	multipartURL := server.URL + "/bucket/tagged-multipart.bin"
	resp, body = do(t, http.MethodPost, multipartURL+"?uploads", nil, map[string]string{
		"x-amz-tagging": "kind=multipart",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("multipart init status = %d, body=%s", resp.StatusCode, body)
	}
	var initialized initiateMultipartUploadResult
	if err := xml.Unmarshal(body, &initialized); err != nil {
		t.Fatal(err)
	}
	resp, _ = do(
		t, http.MethodPut,
		multipartURL+"?partNumber=1&uploadId="+initialized.UploadID,
		[]byte("part"), nil,
	)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("multipart part status = %d", resp.StatusCode)
	}
	manifest, _ := xml.Marshal(completeMultipartUpload{
		Parts: []completePartItem{{PartNumber: 1, ETag: resp.Header.Get("ETag")}},
	})
	resp, body = do(
		t, http.MethodPost,
		multipartURL+"?uploadId="+initialized.UploadID,
		manifest, nil,
	)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("multipart complete status = %d, body=%s", resp.StatusCode, body)
	}
	assertObjectTags(t, multipartURL, map[string]string{"kind": "multipart"})
}

func TestCopyObjectTaggingDirective(t *testing.T) {
	server := newTestServer(t)
	sourceURL := server.URL + "/bucket/source.txt"
	resp, _ := do(t, http.MethodPut, sourceURL, []byte("copy"), map[string]string{
		"x-amz-tagging": "source=yes",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("source PUT status = %d", resp.StatusCode)
	}

	copiedURL := server.URL + "/bucket/copied.txt"
	resp, body := do(t, http.MethodPut, copiedURL, nil, map[string]string{
		"x-amz-copy-source": "/bucket/source.txt",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("copy status = %d, body=%s", resp.StatusCode, body)
	}
	assertObjectTags(t, copiedURL, map[string]string{"source": "yes"})

	replacedURL := server.URL + "/bucket/replaced.txt"
	resp, body = do(t, http.MethodPut, replacedURL, nil, map[string]string{
		"x-amz-copy-source":       "/bucket/source.txt",
		"x-amz-tagging-directive": "REPLACE",
		"x-amz-tagging":           "destination=yes",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("copy replace status = %d, body=%s", resp.StatusCode, body)
	}
	assertObjectTags(t, replacedURL, map[string]string{"destination": "yes"})
}

func assertObjectTags(t *testing.T, objectURL string, want map[string]string) {
	t.Helper()
	resp, body := do(t, http.MethodGet, objectURL+"?tagging", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get tags status = %d, body=%s", resp.StatusCode, body)
	}
	var result tagging
	if err := xml.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string, len(result.TagSet))
	for _, tag := range result.TagSet {
		got[tag.Key] = tag.Value
	}
	if len(got) != len(want) {
		t.Fatalf("tags = %#v, want %#v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("tags = %#v, want %#v", got, want)
		}
	}
}

func TestTagsFromSetRejectsDuplicatesAndLimit(t *testing.T) {
	if _, err := tagsFromSet([]s3Tag{{Key: "same"}, {Key: "same"}}); err == nil {
		t.Fatal("expected duplicate tag error")
	}
	items := make([]s3Tag, 11)
	for i := range items {
		items[i] = s3Tag{Key: string(rune('a' + i))}
	}
	if _, err := tagsFromSet(items); err == nil {
		t.Fatal("expected object tag limit error")
	}
}
