package rest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newPublicRangeDecimalFixture(t *testing.T) (*httptest.Server, string, []string) {
	t.Helper()
	server, alice, _, _ := newEnterpriseRESTTest(t)
	key := "review/decimal.jpg"
	response, body := req(t, http.MethodPut, server.URL+"/v1/files/"+key, []byte("jpeg-image"),
		map[string]string{"Authorization": alice, "Content-Type": "image/jpeg"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upload: status=%d body=%q", response.StatusCode, body)
	}
	share := createWorkbenchShare(t, server, alice, key)
	response, body = req(t, http.MethodPost, server.URL+"/v1/assets",
		[]byte(`{"key":"review/decimal.jpg","slug":"review/decimal.jpg"}`),
		map[string]string{"Authorization": alice, "Content-Type": "application/json"})
	var asset struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &asset); err != nil || response.StatusCode != http.StatusCreated || asset.URL == "" {
		t.Fatalf("publish: status=%d body=%q err=%v", response.StatusCode, body, err)
	}
	return server, alice, []string{share.URL, asset.URL}
}

func TestPublicRangeDecimalMalformedSignsAreIgnored(t *testing.T) {
	_, _, endpoints := newPublicRangeDecimalFixture(t)
	for _, endpoint := range endpoints {
		for _, value := range []string{"bytes=0--1", "bytes=+1-3", "bytes=1-+3", "bytes=-+3"} {
			response, body := req(t, http.MethodGet, endpoint, nil, map[string]string{"Range": value})
			if response.StatusCode != http.StatusOK || string(body) != "jpeg-image" || response.Header.Get("Content-Range") != "" {
				t.Errorf("malformed public range sliced content: range=%q code=%d body=%q", value, response.StatusCode, body)
			}
		}
	}
}

func TestPublicRangeDecimalOversizedCoordinates(t *testing.T) {
	_, _, endpoints := newPublicRangeDecimalFixture(t)
	for _, endpoint := range endpoints {
		for _, check := range []struct {
			header string
			offset int
		}{
			{"bytes=1-9223372036854775808", 1},
			{"bytes=-" + strings.Repeat("9", 128), 0},
		} {
			response, body := req(t, http.MethodGet, endpoint, nil, map[string]string{"Range": check.header})
			want := "jpeg-image"[check.offset:]
			if response.StatusCode != http.StatusPartialContent || string(body) != want || response.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-9/10", check.offset) {
				t.Errorf("large public range lost selected span: range=%q code=%d body=%q headers=%v", check.header, response.StatusCode, body, response.Header)
			}
		}
	}
}

func TestPublicRangeDecimalOversizedStartDoesNotConsumeShareUse(t *testing.T) {
	server, alice, endpoints := newPublicRangeDecimalFixture(t)
	limited := createLimitedShare(t, server.URL, alice, "review/decimal.jpg")
	for _, endpoint := range []string{limited, endpoints[1]} {
		response, _ := req(t, http.MethodGet, endpoint, nil, map[string]string{"Range": "bytes=9223372036854775808-"})
		if response.StatusCode != http.StatusRequestedRangeNotSatisfiable || response.Header.Get("Content-Range") != "bytes */10" {
			t.Errorf("oversized public start returned content: code=%d headers=%v", response.StatusCode, response.Header)
		}
	}
	response, body := req(t, http.MethodGet, limited, nil, nil)
	if response.StatusCode != http.StatusOK || string(body) != "jpeg-image" {
		t.Fatalf("invalid range consumed share use: code=%d body=%q", response.StatusCode, body)
	}
	response, _ = req(t, http.MethodGet, limited, nil, nil)
	if response.StatusCode != http.StatusGone {
		t.Errorf("valid read did not consume share use: code=%d", response.StatusCode)
	}
}
