package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

func TestListBucketVersionsIncludesHistoryAndDeleteMarkers(t *testing.T) {
	svc, _, ts := setupTest(t)
	ctx := context.Background()
	if err := svc.SetBucketVersioning(ctx, "", "archive", true); err != nil {
		t.Fatal(err)
	}
	first := putBucketVersionObject(t, svc, "archive", "doc.txt", "first")
	second := putBucketVersionObject(t, svc, "archive", "doc.txt", "second")
	marker, err := svc.CreateDeleteMarker(ctx, "", "archive", "doc.txt")
	if err != nil {
		t.Fatal(err)
	}
	other := putBucketVersionObject(t, svc, "archive", "other.txt", "other")

	page1 := getBucketVersions(t, ts.URL+
		"/buckets/archive/versions?max-keys=2")
	if len(page1.Versions) != 2 || !page1.HasMore {
		t.Fatalf("page 1 = %+v, want two entries and continuation", page1)
	}
	assertBucketVersion(t, page1.Versions[0], marker.VersionID, true, true)
	assertBucketVersion(t, page1.Versions[1], second.VersionID, false, false)
	if page1.NextKeyMarker != "doc.txt" ||
		page1.NextVersionIDMarker != second.VersionID {
		t.Fatalf("page 1 continuation = %+v", page1)
	}

	endpoint := ts.URL + "/buckets/archive/versions?max-keys=2" +
		"&key-marker=" + url.QueryEscape(page1.NextKeyMarker) +
		"&version-id-marker=" + url.QueryEscape(page1.NextVersionIDMarker)
	page2 := getBucketVersions(t, endpoint)
	if len(page2.Versions) != 2 || page2.HasMore {
		t.Fatalf("page 2 = %+v, want final two entries", page2)
	}
	assertBucketVersion(t, page2.Versions[0], first.VersionID, false, false)
	assertBucketVersion(t, page2.Versions[1], other.VersionID, true, false)
}

func TestListBucketVersionsRejectsOrphanVersionMarker(t *testing.T) {
	_, _, ts := setupTest(t)
	resp, body := req(
		t, http.MethodGet,
		ts.URL+"/buckets/default/versions?version-id-marker=orphan",
		nil, nil,
	)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", resp.StatusCode, body)
	}
}

func TestOpenAPIBucketVersionsParameters(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(globalSpec.JSON(), &spec); err != nil {
		t.Fatal(err)
	}
	paths := spec["paths"].(map[string]any)
	path := paths["/v1/buckets/{bucket}/versions"].(map[string]any)
	get := path["get"].(map[string]any)
	params := get["parameters"].([]any)
	got := make(map[string]string, len(params))
	for _, value := range params {
		param := value.(map[string]any)
		if ref, ok := param["$ref"].(string); ok {
			got[ref] = "ref"
			continue
		}
		got[param["name"].(string)] = param["in"].(string)
	}
	for name, location := range map[string]string{
		"#/components/parameters/bucket": "ref",
		"prefix":                         "query",
		"key-marker":                     "query",
		"version-id-marker":              "query",
		"max-keys":                       "query",
	} {
		if got[name] != location {
			t.Fatalf("parameter %q location=%q, want %q; all=%v",
				name, got[name], location, got)
		}
	}
}

func putBucketVersionObject(
	t *testing.T,
	svc *service.FileService,
	bucket, key, body string,
) repository.Object {
	t.Helper()
	obj, err := svc.Put(
		context.Background(), "", bucket, key,
		strings.NewReader(body), int64(len(body)), service.PutOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

func getBucketVersions(t *testing.T, endpoint string) bucketVersionsResponse {
	t.Helper()
	resp, body := req(t, http.MethodGet, endpoint, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status=%d, want 200; body=%s", resp.StatusCode, body)
	}
	var out bucketVersionsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertBucketVersion(
	t *testing.T,
	got bucketVersionEntry,
	versionID string,
	isLatest, isDeleteMarker bool,
) {
	t.Helper()
	if got.VersionID != versionID ||
		got.IsLatest != isLatest ||
		got.DeleteMarker != isDeleteMarker {
		t.Fatalf(
			"version = %+v, want id=%q latest=%t delete_marker=%t",
			got, versionID, isLatest, isDeleteMarker,
		)
	}
}
