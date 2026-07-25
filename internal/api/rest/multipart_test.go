package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMultipartEndpoints(t *testing.T) {
	_, _, ts := setupTest(t)

	initURL := ts.URL + "/multipart"
	resp, body := req(t, "POST", initURL, []byte(`{"key":"bigfile.bin"}`), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("init multipart: status=%d want 201, body=%s", resp.StatusCode, body)
	}
	var initResp initMultipartResponse
	if err := json.Unmarshal(body, &initResp); err != nil {
		t.Fatalf("init multipart: decode: %v", err)
	}
	if initResp.UploadID == "" {
		t.Fatal("init multipart: upload_id is empty")
	}
	if initResp.Key != "bigfile.bin" {
		t.Errorf("init multipart: key=%q want bigfile.bin", initResp.Key)
	}

	partURL := ts.URL + "/multipart/" + initResp.UploadID + "/parts/1"
	resp, body = req(t, "PUT", partURL, []byte("part1 data"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload part: status=%d want 200, body=%s", resp.StatusCode, body)
	}
	var partResp partResponse
	if err := json.Unmarshal(body, &partResp); err != nil {
		t.Fatalf("upload part: decode: %v", err)
	}
	if partResp.PartNumber != 1 {
		t.Errorf("upload part: part_number=%d want 1", partResp.PartNumber)
	}
	if partResp.ETag == "" {
		t.Error("upload part: etag is empty")
	}

	completeURL := ts.URL + "/multipart/" + initResp.UploadID + "/complete"
	resp, body = req(t, "POST", completeURL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete multipart: status=%d want 200, body=%s", resp.StatusCode, body)
	}
	var obj objectDTO
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("complete multipart: decode: %v", err)
	}
	if obj.Key != "bigfile.bin" {
		t.Errorf("complete multipart: key=%q want bigfile.bin", obj.Key)
	}

	resp, body = req(t, "GET", ts.URL+"/files/bigfile.bin", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET after multipart: status=%d want 200", resp.StatusCode)
	}
	if string(body) != "part1 data" {
		t.Errorf("GET after multipart: body=%q want part1 data", body)
	}
}

func TestAbortMultipart(t *testing.T) {
	_, _, ts := setupTest(t)

	resp, body := req(t, "POST", ts.URL+"/multipart", []byte(`{"key":"abortme.bin"}`), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("init multipart: status=%d", resp.StatusCode)
	}
	var initResp initMultipartResponse
	json.Unmarshal(body, &initResp)

	partURL := ts.URL + "/multipart/" + initResp.UploadID + "/parts/1"
	resp, _ = req(t, "PUT", partURL, []byte("data"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload part: status=%d", resp.StatusCode)
	}

	abortURL := ts.URL + "/multipart/" + initResp.UploadID
	resp, body = req(t, "DELETE", abortURL, nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("abort multipart: status=%d want 204, body=%s", resp.StatusCode, body)
	}
}

func TestMultipartInvalidInit(t *testing.T) {
	_, _, ts := setupTest(t)
	resp, body := req(t, "POST", ts.URL+"/multipart", []byte(`not-json`), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("multipart init bad JSON: status=%d want 400, body=%s", resp.StatusCode, body)
	}
}

func TestMultipartInvalidPartNumber(t *testing.T) {
	_, _, ts := setupTest(t)

	resp, body := req(t, "POST", ts.URL+"/multipart", []byte(`{"key":"badparts.bin"}`), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("init: status=%d", resp.StatusCode)
	}
	var initResp initMultipartResponse
	json.Unmarshal(body, &initResp)

	resp, body = req(t, "PUT", ts.URL+"/multipart/"+initResp.UploadID+"/parts/0", []byte("data"), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("part number 0: status=%d want 400, body=%s", resp.StatusCode, body)
	}
}

func TestPostFormMultipartUpload(t *testing.T) {
	_, _, ts := setupTest(t)

	boundary := "testboundary"
	formBody := "--" + boundary + "\r\n" +
		`Content-Disposition: form-data; name="file"; filename="formfile.txt"` + "\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"uploaded via form" + "\r\n" +
		"--" + boundary + "--\r\n"

	resp, body := req(t, "POST", ts.URL+"/files", []byte(formBody), map[string]string{
		"Content-Type": "multipart/form-data; boundary=" + boundary,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST form: status=%d want 201, body=%s", resp.StatusCode, body)
	}
	var obj objectDTO
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("POST form: decode: %v", err)
	}
	if obj.Key != "formfile.txt" {
		t.Errorf("POST form: key=%q want formfile.txt", obj.Key)
	}

	resp, body = req(t, "GET", ts.URL+"/files/formfile.txt", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET after form: status=%d want 200", resp.StatusCode)
	}
	if string(body) != "uploaded via form" {
		t.Errorf("GET after form: body=%q want uploaded via form", body)
	}
}

func TestBucketACL(t *testing.T) {
	_, _, ts := setupTest(t)

	resp, body := req(t, "GET", ts.URL+"/buckets/default/acl", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET bucket acl: status=%d want 200, body=%s", resp.StatusCode, body)
	}

	resp, body = req(t, "PUT", ts.URL+"/buckets/default/acl", []byte(`{"acl":"public-read"}`), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT bucket acl: status=%d want 200, body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "public-read") {
		t.Errorf("PUT bucket acl: body=%s want public-read", body)
	}

	resp, body = req(t, "GET", ts.URL+"/buckets/default/acl", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET bucket acl after set: status=%d want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "public-read") {
		t.Errorf("GET bucket acl after set: body=%s want public-read", body)
	}
}

func TestBucketACLInvalid(t *testing.T) {
	_, _, ts := setupTest(t)
	resp, body := req(t, "PUT", ts.URL+"/buckets/default/acl", []byte(`{"acl":"bogus"}`), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT bucket acl invalid: status=%d want 400, body=%s", resp.StatusCode, body)
	}
}

func TestGetObjectACL(t *testing.T) {
	_, _, ts := setupTest(t)

	resp, _ := req(t, "PUT", ts.URL+"/files/acl-test.txt", []byte("data"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: status=%d", resp.StatusCode)
	}

	resp, body := req(t, "GET", ts.URL+"/files/acl-test.txt/acl", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET object acl: status=%d want 200, body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "private") {
		t.Errorf("GET object acl: body=%s want private", body)
	}
}

func TestOpenAPISpec(t *testing.T) {
	h := OpenAPISpecHandler()
	srv := httptest.NewServer(http.HandlerFunc(h.ServeHTTP))
	defer srv.Close()

	resp, body := req(t, "GET", srv.URL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("openapi: status=%d want 200", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("openapi: Content-Type=%q want application/json", resp.Header.Get("Content-Type"))
	}
	if len(body) == 0 {
		t.Error("openapi: empty body")
	}
	var spec map[string]any
	if err := json.Unmarshal(body, &spec); err != nil {
		t.Fatalf("openapi: decode: %v", err)
	}
}

func TestSwaggerUI(t *testing.T) {
	h := SwaggerUIHandler()
	srv := httptest.NewServer(http.HandlerFunc(h.ServeHTTP))
	defer srv.Close()

	resp, body := req(t, "GET", srv.URL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("swagger: status=%d want 200", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("swagger: Content-Type=%q want text/html", resp.Header.Get("Content-Type"))
	}
	if len(body) == 0 {
		t.Error("swagger: empty body")
	}
}
