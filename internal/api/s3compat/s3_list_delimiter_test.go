package s3compat

import (
	"bytes"
	"encoding/xml"
	"net/http"
	"net/url"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestListObjectsV2DelimiterGroupsKeys(t *testing.T) {
	server := newTestServer(t)
	base := server.URL + "/bucket"
	putListKeys(t, base, []string{
		"photos/2006/february/one.jpg",
		"photos/2006/february/two.jpg",
		"photos/2006/january/one.jpg",
		"photos/2006/readme.txt",
		"photos/2007/archive.jpg",
	})

	query := url.Values{
		"list-type": {"2"},
		"prefix":    {"photos/2006/"},
		"delimiter": {"/"},
	}
	response, body := do(t, http.MethodGet, base+"?"+query.Encode(), nil, nil)
	result := decodeListV2(t, response, body)
	if result.Delimiter != "/" || result.KeyCount != 3 || result.IsTruncated {
		t.Fatalf("unexpected list metadata: %+v body=%s", result, body)
	}
	if len(result.Contents) != 1 || result.Contents[0].Key != "photos/2006/readme.txt" {
		t.Fatalf("contents=%+v body=%s", result.Contents, body)
	}
	wantPrefixes := []string{"photos/2006/february/", "photos/2006/january/"}
	assertCommonPrefixes(t, result.CommonPrefixes, wantPrefixes)
}

func TestListObjectsV2DelimiterPaginationAndStartAfter(t *testing.T) {
	server := newTestServer(t)
	base := server.URL + "/bucket"
	putListKeys(t, base, []string{"a.txt", "b/1.txt", "b/2.txt", "c.txt", "d/1.txt"})

	firstQuery := url.Values{"list-type": {"2"}, "delimiter": {"/"}, "max-keys": {"2"}}
	response, body := do(t, http.MethodGet, base+"?"+firstQuery.Encode(), nil, nil)
	first := decodeListV2(t, response, body)
	if first.KeyCount != 2 || !first.IsTruncated || first.NextContinuationToken == "" {
		t.Fatalf("unexpected first page: %+v body=%s", first, body)
	}
	if len(first.Contents) != 1 || first.Contents[0].Key != "a.txt" {
		t.Fatalf("first contents=%+v", first.Contents)
	}
	assertCommonPrefixes(t, first.CommonPrefixes, []string{"b/"})

	secondQuery := url.Values{
		"list-type":          {"2"},
		"delimiter":          {"/"},
		"max-keys":           {"2"},
		"continuation-token": {first.NextContinuationToken},
	}
	response, body = do(t, http.MethodGet, base+"?"+secondQuery.Encode(), nil, nil)
	second := decodeListV2(t, response, body)
	if second.KeyCount != 2 || second.IsTruncated {
		t.Fatalf("unexpected second page: %+v body=%s", second, body)
	}
	if len(second.Contents) != 1 || second.Contents[0].Key != "c.txt" {
		t.Fatalf("second contents=%+v", second.Contents)
	}
	assertCommonPrefixes(t, second.CommonPrefixes, []string{"d/"})

	startQuery := url.Values{
		"list-type": {"2"}, "delimiter": {"/"}, "start-after": {"b/1.txt"},
	}
	response, body = do(t, http.MethodGet, base+"?"+startQuery.Encode(), nil, nil)
	started := decodeListV2(t, response, body)
	if len(started.Contents) != 1 || started.Contents[0].Key != "c.txt" {
		t.Fatalf("start-after contents=%+v body=%s", started.Contents, body)
	}
	assertCommonPrefixes(t, started.CommonPrefixes, []string{"d/"})
}

func TestListObjectsV1DelimiterPagination(t *testing.T) {
	server := newTestServer(t)
	base := server.URL + "/bucket"
	putListKeys(t, base, []string{"a/1.txt", "a/2.txt", "b.txt", "c/1.txt"})

	query := url.Values{"delimiter": {"/"}, "max-keys": {"1"}}
	response, body := do(t, http.MethodGet, base+"?"+query.Encode(), nil, nil)
	first := decodeListV1(t, response, body)
	if !first.IsTruncated || first.NextMarker == "" || len(first.Contents) != 0 {
		t.Fatalf("unexpected first page: %+v body=%s", first, body)
	}
	assertCommonPrefixes(t, first.CommonPrefixes, []string{"a/"})

	query.Set("marker", first.NextMarker)
	response, body = do(t, http.MethodGet, base+"?"+query.Encode(), nil, nil)
	second := decodeListV1(t, response, body)
	if !second.IsTruncated || len(second.Contents) != 1 || second.Contents[0].Key != "b.txt" {
		t.Fatalf("unexpected second page: %+v body=%s", second, body)
	}
	if len(second.CommonPrefixes) != 0 || second.Marker != first.NextMarker {
		t.Fatalf("second page repeated prefix or marker drifted: %+v", second)
	}

	query.Set("marker", second.NextMarker)
	response, body = do(t, http.MethodGet, base+"?"+query.Encode(), nil, nil)
	third := decodeListV1(t, response, body)
	if third.IsTruncated || len(third.Contents) != 0 {
		t.Fatalf("unexpected third page: %+v body=%s", third, body)
	}
	assertCommonPrefixes(t, third.CommonPrefixes, []string{"c/"})
}

func TestListObjectsV2DelimiterCombinesWithTagFilter(t *testing.T) {
	server := newTestServer(t)
	base := server.URL + "/bucket"
	for key, tags := range map[string]string{
		"a/match.txt": "env=prod",
		"a/skip.txt":  "env=dev",
		"b.txt":       "env=prod",
	} {
		response, body := do(t, http.MethodPut, base+"/"+key, []byte(key), map[string]string{
			"x-amz-tagging": tags,
		})
		if response.StatusCode != http.StatusOK {
			t.Fatalf("put %s status=%d body=%s", key, response.StatusCode, body)
		}
	}

	query := url.Values{
		"list-type": {"2"}, "delimiter": {"/"},
		"tag-key": {"env"}, "tag-value": {"prod"},
	}
	response, body := do(t, http.MethodGet, base+"?"+query.Encode(), nil, nil)
	result := decodeListV2(t, response, body)
	if result.KeyCount != 2 || len(result.Contents) != 1 || result.Contents[0].Key != "b.txt" {
		t.Fatalf("unexpected tagged list: %+v body=%s", result, body)
	}
	assertCommonPrefixes(t, result.CommonPrefixes, []string{"a/"})
}

func TestListObjectsWithoutDelimiterOmitsDelimiterElement(t *testing.T) {
	server := newTestServer(t)
	base := server.URL + "/bucket"
	putListKeys(t, base, []string{"a/one.txt"})

	for _, query := range []string{"?list-type=2", ""} {
		response, body := do(t, http.MethodGet, base+query, nil, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("list %q status=%d body=%s", query, response.StatusCode, body)
		}
		if bytes.Contains(body, []byte("<Delimiter>")) || bytes.Contains(body, []byte("<CommonPrefixes>")) {
			t.Fatalf("list %q emitted delimiter-only elements: %s", query, body)
		}
	}
}

func TestDelimitedListCarriesGroupAcrossRawPages(t *testing.T) {
	pages := map[string]repository.ListPage{
		"": {
			Objects:    []repository.Object{{Key: "a/1.txt"}},
			NextMarker: "a/1.txt", HasMore: true,
		},
		"a/1.txt": {
			Objects: []repository.Object{{Key: "a/2.txt"}, {Key: "b.txt"}},
		},
	}
	fetch := func(marker string, _ int) (repository.ListPage, error) {
		return pages[marker], nil
	}

	page, err := loadObjectListPage("", "/", "", 1, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || page.NextMarker != "a/2.txt" || len(page.Objects) != 0 {
		t.Fatalf("unexpected collapsed page: %+v", page)
	}
	assertCommonPrefixes(t, page.CommonPrefixes, []string{"a/"})
}

func putListKeys(t *testing.T, base string, keys []string) {
	t.Helper()
	for _, key := range keys {
		response, body := do(t, http.MethodPut, base+"/"+key, []byte(key), nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("put %s status=%d body=%s", key, response.StatusCode, body)
		}
	}
}

func decodeListV2(t *testing.T, response *http.Response, body []byte) listBucketResult {
	t.Helper()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d body=%s", response.StatusCode, body)
	}
	var result listBucketResult
	if err := xml.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode ListObjectsV2: %v body=%s", err, body)
	}
	return result
}

func decodeListV1(t *testing.T, response *http.Response, body []byte) listBucketResultV1 {
	t.Helper()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d body=%s", response.StatusCode, body)
	}
	var result listBucketResultV1
	if err := xml.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode ListObjects: %v body=%s", err, body)
	}
	return result
}

func assertCommonPrefixes(t *testing.T, got []commonPrefix, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("common prefixes=%+v want=%v", got, want)
	}
	for index := range want {
		if got[index].Prefix != want[index] {
			t.Fatalf("common prefix[%d]=%q want=%q", index, got[index].Prefix, want[index])
		}
	}
}
