package s3compat

import (
	"encoding/xml"
	"net/http"
	"testing"
)

func TestBucketAccelerateRoundTrip(t *testing.T) {
	srv := newTestServer(t)
	url := srv.URL + "/accelerated?accelerate"
	resp, body := do(t, http.MethodGet, url, nil, nil)
	var got accelerateConfig
	if err := xml.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || got.Status != "Suspended" {
		t.Fatalf("default accelerate status=%d config=%+v body=%s", resp.StatusCode, got, body)
	}
	input, _ := xml.Marshal(accelerateConfig{Status: "Enabled"})
	resp, body = do(t, http.MethodPut, url, input, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put accelerate status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = do(t, http.MethodGet, url, nil, nil)
	if err := xml.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "Enabled" {
		t.Fatalf("accelerate status did not persist: %+v body=%s", got, body)
	}
	input, _ = xml.Marshal(accelerateConfig{Status: "Invalid"})
	resp, _ = do(t, http.MethodPut, url, input, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid accelerate status=%d", resp.StatusCode)
	}
}
