package s3compat

import (
	"encoding/xml"
	"net/http"
	"strings"
	"testing"
)

func TestSSECMultipartRoundTrip(t *testing.T) {
	server := newTestServer(t)
	key := []byte("0123456789abcdef0123456789abcdef")
	headers := ssecTestHeaders(key)
	headers["Content-Type"] = "application/octet-stream"
	headers["x-amz-storage-class"] = "STANDARD_IA"
	objectURL := server.URL + "/bucket/multipart.bin"

	resp, body := do(t, http.MethodPost, objectURL+"?uploads", nil, headers)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create multipart status = %d, body = %s", resp.StatusCode, body)
	}
	var initialized initiateMultipartUploadResult
	if err := xml.Unmarshal(body, &initialized); err != nil {
		t.Fatal(err)
	}
	if initialized.UploadID == "" {
		t.Fatal("empty upload id")
	}

	partURL := objectURL + "?partNumber=1&uploadId=" + initialized.UploadID
	resp, _ = do(t, http.MethodPut, partURL, []byte("encrypted multipart"), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("part without SSE-C status = %d, want 400", resp.StatusCode)
	}
	resp, _ = do(t, http.MethodPut, partURL, []byte("encrypted multipart"), headers)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("part with SSE-C status = %d", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")

	manifest, err := xml.Marshal(completeMultipartUpload{
		Parts: []completePartItem{{PartNumber: 1, ETag: etag}},
	})
	if err != nil {
		t.Fatal(err)
	}
	completeURL := objectURL + "?uploadId=" + initialized.UploadID
	resp, body = do(t, http.MethodPost, completeURL, manifest, headers)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete multipart status = %d, body = %s", resp.StatusCode, body)
	}
	assertSSECResponseHeaders(t, resp, headers)

	resp, body = do(t, http.MethodGet, objectURL, nil, headers)
	if resp.StatusCode != http.StatusOK || string(body) != "encrypted multipart" {
		t.Fatalf("multipart GET status = %d, body = %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("multipart content type = %q", got)
	}
	if got := resp.Header.Get("x-amz-storage-class"); got != "STANDARD_IA" {
		t.Fatalf("multipart storage class = %q", got)
	}
}

func TestSSECCopyCanDecryptAndReencrypt(t *testing.T) {
	server := newTestServer(t)
	sourceKey := []byte("0123456789abcdef0123456789abcdef")
	destinationKey := []byte("abcdef0123456789abcdef0123456789")
	sourceURL := server.URL + "/source/encrypted.txt"
	resp, body := do(t, http.MethodPut, sourceURL, []byte("copy secret"), ssecTestHeaders(sourceKey))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("source PUT status = %d, body=%s", resp.StatusCode, body)
	}

	plainCopyURL := server.URL + "/dest/plain.txt"
	copyHeaders := ssecHeadersForPrefix(sourceKey, ssecCopyHeaderPrefix)
	copyHeaders["x-amz-copy-source"] = "/source/encrypted.txt"
	resp, body = do(t, http.MethodPut, plainCopyURL, nil, copyHeaders)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("plaintext copy status = %d, body=%s", resp.StatusCode, body)
	}
	resp, body = do(t, http.MethodGet, plainCopyURL, nil, nil)
	if resp.StatusCode != http.StatusOK || string(body) != "copy secret" {
		t.Fatalf("plaintext copy GET status = %d, body=%q", resp.StatusCode, body)
	}

	encryptedCopyURL := server.URL + "/dest/reencrypted.txt"
	copyHeaders = ssecHeadersForPrefix(sourceKey, ssecCopyHeaderPrefix)
	for name, value := range ssecTestHeaders(destinationKey) {
		copyHeaders[name] = value
	}
	copyHeaders["x-amz-copy-source"] = "/source/encrypted.txt"
	resp, body = do(t, http.MethodPut, encryptedCopyURL, nil, copyHeaders)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("encrypted copy status = %d, body=%s", resp.StatusCode, body)
	}
	resp, _ = do(t, http.MethodGet, encryptedCopyURL, nil, ssecTestHeaders(sourceKey))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("reencrypted copy accepted source key, status = %d", resp.StatusCode)
	}
	resp, body = do(t, http.MethodGet, encryptedCopyURL, nil, ssecTestHeaders(destinationKey))
	if resp.StatusCode != http.StatusOK || string(body) != "copy secret" {
		t.Fatalf("reencrypted copy GET status = %d, body=%q", resp.StatusCode, body)
	}
}

func TestSSECUploadPartCopyAcrossBuckets(t *testing.T) {
	server := newTestServer(t)
	sourceKey := []byte("0123456789abcdef0123456789abcdef")
	destinationKey := []byte("abcdef0123456789abcdef0123456789")
	sourceURL := server.URL + "/source/part.txt"
	resp, body := do(t, http.MethodPut, sourceURL, []byte("part-copy-secret"), ssecTestHeaders(sourceKey))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("source PUT status = %d, body=%s", resp.StatusCode, body)
	}

	destinationURL := server.URL + "/dest/copied.bin"
	resp, body = do(t, http.MethodPost, destinationURL+"?uploads", nil, ssecTestHeaders(destinationKey))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("multipart init status = %d, body=%s", resp.StatusCode, body)
	}
	var initialized initiateMultipartUploadResult
	if err := xml.Unmarshal(body, &initialized); err != nil {
		t.Fatal(err)
	}
	copyHeaders := ssecHeadersForPrefix(sourceKey, ssecCopyHeaderPrefix)
	for name, value := range ssecTestHeaders(destinationKey) {
		copyHeaders[name] = value
	}
	copyHeaders["x-amz-copy-source"] = "/source/part.txt"
	partURL := destinationURL + "?partNumber=1&uploadId=" + initialized.UploadID
	resp, body = do(t, http.MethodPut, partURL, nil, copyHeaders)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload part copy status = %d, body=%s", resp.StatusCode, body)
	}
	var copied copyPartResult
	if err := xml.Unmarshal(body, &copied); err != nil {
		t.Fatal(err)
	}
	manifest, _ := xml.Marshal(completeMultipartUpload{
		Parts: []completePartItem{{PartNumber: 1, ETag: copied.ETag}},
	})
	resp, body = do(
		t, http.MethodPost,
		destinationURL+"?uploadId="+initialized.UploadID,
		manifest, ssecTestHeaders(destinationKey),
	)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete copied multipart status = %d, body=%s", resp.StatusCode, body)
	}
	resp, body = do(t, http.MethodGet, destinationURL, nil, ssecTestHeaders(destinationKey))
	if resp.StatusCode != http.StatusOK || string(body) != "part-copy-secret" {
		t.Fatalf("copied multipart GET status = %d, body=%q", resp.StatusCode, body)
	}
}

func ssecHeadersForPrefix(key []byte, prefix string) map[string]string {
	headers := ssecTestHeaders(key)
	out := make(map[string]string, len(headers))
	for name, value := range headers {
		out[strings.Replace(name, ssecHeaderPrefix, prefix, 1)] = value
	}
	return out
}
