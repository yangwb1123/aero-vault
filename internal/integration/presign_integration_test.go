package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestFullServer_PresignedPutUsesFileService(t *testing.T) {
	ts := startFullServer(t)
	key := "presigned/folder/a file.txt"
	presignURL := ts.URL + "/v1/files/presigned/folder/a%20file.txt/presign?op=put&expires=60"
	resp, err := http.Post(presignURL, "application/json", nil)
	if err != nil {
		t.Fatalf("request presigned PUT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("presign status=%d body=%s", resp.StatusCode, body)
	}
	var signed struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&signed); err != nil {
		t.Fatalf("decode presign response: %v", err)
	}
	if signed.URL == "" || !strings.Contains(signed.URL, "x-aero-presign-signature=") {
		t.Fatalf("unexpected presigned URL %q", signed.URL)
	}

	const body = "presigned upload through FileService"
	putResp, err := httpPut(signed.URL, "text/plain", body)
	if err != nil {
		t.Fatalf("use presigned PUT: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusCreated {
		t.Fatalf("presigned PUT status=%d want 201", putResp.StatusCode)
	}

	getResp, err := http.Get(ts.URL + "/v1/files/presigned/folder/a%20file.txt")
	if err != nil {
		t.Fatalf("REST GET: %v", err)
	}
	got, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK || string(got) != body {
		t.Fatalf("REST GET status=%d body=%q", getResp.StatusCode, got)
	}
	if gotLength := getResp.Header.Get("Content-Length"); gotLength != "36" {
		t.Fatalf("Content-Length=%q want 36", gotLength)
	}

	s3Resp, err := http.Get(ts.URL + "/s3/default/" + key)
	if err != nil {
		t.Fatalf("S3 GET: %v", err)
	}
	s3Body, _ := io.ReadAll(s3Resp.Body)
	s3Resp.Body.Close()
	if s3Resp.StatusCode != http.StatusOK || string(s3Body) != body {
		t.Fatalf("S3 GET status=%d body=%q", s3Resp.StatusCode, s3Body)
	}
}

func TestFullServer_PresignedGetUsesFileService(t *testing.T) {
	ts := startFullServer(t)
	const objectURL = "/v1/files/presigned/download%20me.txt"
	putResp, err := httpPut(ts.URL+objectURL, "text/plain", "capability download")
	if err != nil {
		t.Fatalf("setup PUT: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusCreated {
		t.Fatalf("setup PUT status=%d", putResp.StatusCode)
	}

	resp, err := http.Post(ts.URL+objectURL+"/presign?op=get&expires=60", "application/json", nil)
	if err != nil {
		t.Fatalf("request presigned GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("presign status=%d body=%s", resp.StatusCode, body)
	}
	var signed struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&signed); err != nil {
		t.Fatalf("decode presign response: %v", err)
	}
	if !strings.HasPrefix(signed.URL, ts.URL+objectURL) ||
		!strings.Contains(signed.URL, "x-aero-presign-operation=get") {
		t.Fatalf("unexpected application capability URL %q", signed.URL)
	}

	getResp, err := http.Get(signed.URL)
	if err != nil {
		t.Fatalf("use presigned GET: %v", err)
	}
	body, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK || string(body) != "capability download" {
		t.Fatalf("presigned GET status=%d body=%q", getResp.StatusCode, body)
	}
	headReq, _ := http.NewRequest(http.MethodHead, signed.URL, nil)
	headResp, err := http.DefaultClient.Do(headReq)
	if err != nil {
		t.Fatalf("use presigned HEAD: %v", err)
	}
	headResp.Body.Close()
	if headResp.StatusCode != http.StatusOK || headResp.Header.Get("Content-Length") != "19" {
		t.Fatalf("presigned HEAD status=%d length=%q", headResp.StatusCode, headResp.Header.Get("Content-Length"))
	}

	tampered, err := url.Parse(signed.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := tampered.Query()
	q.Set("version", "another-version")
	tampered.RawQuery = q.Encode()
	tamperedResp, err := http.Get(tampered.String())
	if err != nil {
		t.Fatalf("tampered GET: %v", err)
	}
	tamperedResp.Body.Close()
	if tamperedResp.StatusCode != http.StatusForbidden {
		t.Fatalf("tampered GET status=%d want 403", tamperedResp.StatusCode)
	}
}
