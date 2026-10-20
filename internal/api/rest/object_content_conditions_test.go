package rest

import (
	"bytes"
	"encoding/json"
	"image/jpeg"
	"net/http"
	"strconv"
	"testing"
)

func TestObjectContentSelectionReadConditionsStayPinned(t *testing.T) {
	svc, server := newContentSelectionTest(t)
	old := putContentSelectionFixture(t, svc, "literal/tags", []byte("old bytes"), "text/plain")
	newer := putContentSelectionFixture(t, svc, old.Key, []byte("newer bytes"), "text/plain")
	selected := contentSelectionURL(server.URL, old.Key, "content", old.VersionID)
	for _, test := range []struct {
		headers map[string]string
		status  int
		body    string
	}{
		{map[string]string{"Range": "bytes=0-2"}, http.StatusPartialContent, "old"},
		{map[string]string{"Range": "bytes=0-2", "If-Range": `"` + old.ETag + `"`}, http.StatusPartialContent, "old"},
		{map[string]string{"Range": "bytes=0-2", "If-Range": `"` + newer.ETag + `"`}, http.StatusOK, "old bytes"},
		{map[string]string{"Range": "bytes=0-2", "If-None-Match": `"` + old.ETag + `"`}, http.StatusNotModified, ""},
		{map[string]string{"Range": "bytes=0-2", "If-Match": `"` + newer.ETag + `"`}, http.StatusPreconditionFailed, ""},
		{map[string]string{"Range": "bytes=100-"}, http.StatusRequestedRangeNotSatisfiable, ""},
	} {
		response, body := req(t, http.MethodGet, selected, nil, test.headers)
		if response.StatusCode != test.status || test.body != "" && string(body) != test.body {
			t.Fatalf("selected read headers=%v status=%d body=%q", test.headers, response.StatusCode, body)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		response, body := req(t, method, selected, nil, map[string]string{"If-None-Match": `"` + old.ETag + `"`})
		if response.StatusCode != http.StatusNotModified || len(body) != 0 {
			t.Fatalf("selected validator method=%s status=%d body=%q", method, response.StatusCode, body)
		}
	}
}

func TestObjectContentSelectionThumbnailPinAndHead(t *testing.T) {
	svc, server := newContentSelectionTest(t)
	old := putContentSelectionFixture(t, svc, "photo/metadata", pngBytes(t, 32, 24), "image/png")
	putContentSelectionFixture(t, svc, old.Key, pngBytes(t, 64, 48), "image/png")
	putContentSelectionFixture(t, svc, old.Key+"/thumbnail", []byte("unrelated neighbour"), "text/plain")
	selected := contentSelectionURL(server.URL, old.Key, "thumbnail", old.VersionID) + "&w=64&h=64"
	response, body := req(t, http.MethodGet, selected, nil, nil)
	image, err := jpeg.Decode(bytes.NewReader(body))
	if err != nil || response.StatusCode != http.StatusOK || image.Bounds().Dx() != 32 || image.Bounds().Dy() != 24 {
		t.Fatalf("thumbnail did not stay pinned status=%d err=%v", response.StatusCode, err)
	}
	head, headBody := req(t, http.MethodHead, selected, nil, nil)
	if head.StatusCode != http.StatusOK || len(headBody) != 0 || head.Header.Get("Content-Type") != "image/jpeg" ||
		head.Header.Get("ETag") != response.Header.Get("ETag") || head.Header.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Fatalf("thumbnail HEAD lost GET parity: headers=%v body=%q", head.Header, headBody)
	}
}

func TestObjectContentSelectionOpenAPIParametersAndStream(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(globalSpec.JSON(), &spec); err != nil {
		t.Fatal(err)
	}
	path := spec["paths"].(map[string]any)["/v1/files/{key}"].(map[string]any)
	for _, method := range []string{"get", "head"} {
		operation := path[method].(map[string]any)
		parameters := map[string]bool{}
		for _, value := range operation["parameters"].([]any) {
			parameter := value.(map[string]any)
			if parameter["in"] == "query" {
				parameters[parameter["name"].(string)] = true
			}
		}
		for _, name := range []string{"representation", "version", "w", "h"} {
			if !parameters[name] {
				t.Fatalf("%s OpenAPI missing %s", method, name)
			}
		}
	}
	operation := path["get"].(map[string]any)
	for _, status := range []string{"200", "206"} {
		response := operation["responses"].(map[string]any)[status].(map[string]any)
		content := response["content"].(map[string]any)["*/*"].(map[string]any)
		if content["schema"].(map[string]any)["format"] != "binary" {
			t.Fatalf("%s file bytes documented as JSON metadata", status)
		}
	}
}
