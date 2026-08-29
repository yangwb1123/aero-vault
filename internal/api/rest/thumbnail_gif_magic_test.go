package rest

import (
	"bytes"
	"net/http"
	"testing"
)

func TestThumbnailGenericContentTypeFakeGIFReturns400(t *testing.T) {
	srv := newRESTTest(t)
	u := srv.URL + "/v1/files/fake.gif"
	fakeGIF := []byte("GIF90a-not-a-real-gif")
	resp, _ := req(t, http.MethodPut, u, fakeGIF, map[string]string{"Content-Type": "application/octet-stream"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status=%d, want %d", resp.StatusCode, http.StatusCreated)
	}
	resp, body := req(t, http.MethodGet, u+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("thumbnail status=%d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	if !bytes.Contains(body, []byte(`"code":"InvalidArgument"`)) {
		t.Fatalf("thumbnail body=%s, want InvalidArgument", body)
	}
}
