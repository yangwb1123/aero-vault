package s3compat

import (
	"encoding/xml"
	"net/http"
	"testing"
)

func TestExpectedBucketOwnerAppliesToObjectRequests(t *testing.T) {
	server := newTestServer(t)
	base := server.URL
	do(t, http.MethodPut, base+"/bucket/object.txt", []byte("secret"), nil)

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodDelete} {
		response, body := do(t, method, base+"/bucket/object.txt", nil, map[string]string{
			"x-amz-expected-bucket-owner": "another-tenant",
		})
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("%s with wrong owner status=%d body=%s", method, response.StatusCode, body)
		}
	}
	response, body := do(t, http.MethodGet, base+"/bucket/object.txt", nil, nil)
	if response.StatusCode != http.StatusOK || string(body) != "secret" {
		t.Fatalf("wrong-owner DELETE mutated object: status=%d body=%q", response.StatusCode, body)
	}
}

func TestInvalidPutACLRejectedBeforeObjectCreation(t *testing.T) {
	server := newTestServer(t)
	base := server.URL
	response, body := do(t, http.MethodPut, base+"/bucket/object.txt", []byte("body"), map[string]string{
		"x-amz-acl": "not-a-real-acl",
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid ACL status=%d body=%s", response.StatusCode, body)
	}
	response, _ = do(t, http.MethodGet, base+"/bucket/object.txt", nil, nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("invalid ACL still created object: status=%d", response.StatusCode)
	}
}

func TestMultipartCreationAppliesCannedACL(t *testing.T) {
	server := newTestServer(t)
	base := server.URL
	response, body := do(t, http.MethodPost, base+"/bucket/object.txt?uploads", nil, map[string]string{
		"x-amz-acl": "public-read",
	})
	var initiated initiateMultipartUploadResult
	if err := xml.Unmarshal(body, &initiated); err != nil {
		t.Fatalf("decode initiate response: %v body=%s", err, body)
	}
	if response.StatusCode != http.StatusOK || initiated.UploadID == "" {
		t.Fatalf("initiate status=%d body=%s", response.StatusCode, body)
	}

	partURL := base + "/bucket/object.txt?uploadId=" + initiated.UploadID + "&partNumber=1"
	response, _ = do(t, http.MethodPut, partURL, []byte("body"), nil)
	etag := response.Header.Get("ETag")
	if response.StatusCode != http.StatusOK || etag == "" {
		t.Fatalf("upload part status=%d etag=%q", response.StatusCode, etag)
	}
	manifest, _ := xml.Marshal(completeMultipartUpload{Parts: []completePartItem{{
		PartNumber: 1,
		ETag:       etag,
	}}})
	response, body = do(
		t, http.MethodPost,
		base+"/bucket/object.txt?uploadId="+initiated.UploadID,
		manifest, nil,
	)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("complete status=%d body=%s", response.StatusCode, body)
	}
	response, body = do(t, http.MethodGet, base+"/bucket/object.txt?acl", nil, nil)
	var policy accessControlPolicy
	if err := xml.Unmarshal(body, &policy); err != nil {
		t.Fatalf("decode ACL: %v body=%s", err, body)
	}
	if response.StatusCode != http.StatusOK || policyToCanned(policy) != "public-read" {
		t.Fatalf("multipart ACL status=%d policy=%+v body=%s", response.StatusCode, policy, body)
	}
}
