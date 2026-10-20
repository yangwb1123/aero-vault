package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestThumbnailCompletedFollowerDeadlineReturnsTimeout(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, selector := range []string{"?representation=thumbnail&w=16&h=16", "/thumbnail?w=16&h=16"} {
			t.Run(method+selector, func(t *testing.T) {
				f := newThumbnailFollowerCancelFixture(t, 2*time.Second)
				path := "/v1/files/" + f.current.Key + selector
				leader := f.start(method, path, "thumbnail-leader", context.Background(), nil)
				waitThumbnailAccessSignal(t, f.entered)
				follower := f.start(method, path, "cancel-follower", context.Background(), nil)
				f.waitJoined(t)
				waitThumbnailAccessSignal(t, f.pause.Context.Done())
				if f.pause.Err() != context.DeadlineExceeded {
					t.Fatalf("deadline did not propagate: %v", f.pause.Err())
				}
				f.unblock()
				assertThumbnailAccessSourceResponse(t, f.thumbnailAccessSourceFixture, collectThumbnailAccessResponse(t, leader), method)
				before := snapshotThumbCounters(t)
				f.pause.unblock()
				result := collectThumbnailAccessResponse(t, follower)
				assertThumbnailCompletedFollowerTimeout(t, result, method)
				if f.finish(t).status != http.StatusGatewayTimeout {
					t.Error("expired completed follower did not write timeout status")
				}
				assertThumbnailFollowerCancelLifecycle(t, f)
				assertThumbDeltas(t, before, "timeout", 0)
			})
		}
	}
}

func TestThumbnailCompletedFollowerOpenAPIDocumentsTimeout(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct{ Description string }
		}
	}
	if err := json.Unmarshal(globalSpec.JSON(), &spec); err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ path, method string }{
		{"/v1/files/{key}", "get"}, {"/v1/files/{key}", "head"}, {"/v1/files/{key}/thumbnail", "get"},
	} {
		description := spec.Paths[route.path][route.method].Responses["504"].Description
		for _, boundary := range []string{"deadline", "completed", "cancellation", "silent"} {
			if !strings.Contains(description, boundary) {
				t.Errorf("%s %s 504 missing %q: %q", route.method, route.path, boundary, description)
			}
		}
	}
}

func assertThumbnailCompletedFollowerTimeout(t *testing.T, result thumbnailAccessHTTPResult, method string) {
	t.Helper()
	if result.response.StatusCode != http.StatusGatewayTimeout {
		t.Errorf("expired completed follower status=%d want 504", result.response.StatusCode)
	}
	for _, name := range []string{"ETag", "Last-Modified", "X-Version-Id", "Cache-Control", "Content-Range"} {
		if result.response.Header.Get(name) != "" {
			t.Errorf("timeout retained success %s=%q", name, result.response.Header.Get(name))
		}
	}
	if method == http.MethodHead {
		if len(result.body) != 0 {
			t.Error("timeout HEAD returned a body")
		}
		return
	}
	var body errorBody
	if err := json.Unmarshal(result.body, &body); err != nil {
		t.Errorf("timeout response is not JSON: %v", err)
		return
	}
	if body.Error.Code != "Timeout" || body.Error.Message != "request timed out" || body.Error.RequestID != "cancel-follower" {
		t.Errorf("timeout lost safe classification or follower request ID: %+v", body)
	}
}

func TestThumbnailCompletedFollowerDeadlinePrecedesOpenedPrecondition(t *testing.T) {
	f := newThumbnailFollowerCancelFixture(t, 2*time.Second)
	path := "/v1/files/" + f.current.Key + "?representation=thumbnail&w=16&h=16"
	leader := f.start(http.MethodGet, path, "thumbnail-leader", context.Background(), nil)
	waitThumbnailAccessSignal(t, f.entered)
	headers := map[string]string{"If-Match": thumbnailEncodingValidator(f.older)}
	follower := f.start(http.MethodGet, path, "cancel-follower", context.Background(), headers)
	f.waitJoined(t)
	waitThumbnailAccessSignal(t, f.pause.Context.Done())
	f.unblock()
	assertThumbnailAccessSourceResponse(t, f.thumbnailAccessSourceFixture, collectThumbnailAccessResponse(t, leader), http.MethodGet)
	before := snapshotThumbCounters(t)
	f.pause.unblock()
	result := collectThumbnailAccessResponse(t, follower)
	assertThumbnailCompletedFollowerTimeout(t, result, http.MethodGet)
	f.finish(t)
	assertThumbnailFollowerCancelLifecycle(t, f)
	assertThumbDeltas(t, before, "timeout", 0)
	if bytes.Contains(result.body, []byte("PreconditionFailed")) {
		t.Error("opened-source precondition replaced an already-expired request")
	}
}
