package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

func TestIdempotencyBoundaryUsesActualMethodSuffixDispatch(t *testing.T) {
	for _, tc := range []struct {
		method, path, key, kind string
		action                  access.Action
	}{
		{"PUT", "/v1/files/a/restore", "a/restore", "object", access.ActionCreate},
		{"PUT", "/v1/files/a/presign", "a/presign", "object", access.ActionCreate},
		{"PUT", "/v1/files/a/tags", "a", "tags", access.ActionWrite},
		{"PUT", "/files/a/acl", "a", "acl", access.ActionManageACL},
		{"PATCH", "/files/a/metadata", "a", "metadata", access.ActionWrite},
		{"DELETE", "/files/a/acl", "a/acl", "empty", access.ActionDelete},
		{"DELETE", "/files/a/versions", "a/versions", "empty", access.ActionDelete},
		{"DELETE", "/files/a?hard=1&version=ignored", "a", "empty", access.ActionDelete},
		{"DELETE", "/files/a/metadata?key=note", "a", "metadata", access.ActionWrite},
		{"DELETE", "/files/a/tags", "a", "empty", access.ActionWrite},
		{"POST", "/files/a/restore", "a", "restore", access.ActionRestore},
		{"POST", "/files/a/lock", "a", "lock", access.ActionWrite},
		{"POST", "/files/a/presign", "a", "presign", access.ActionDownload},
		{"POST", "/files/a/presign?op=put", "a", "presign", access.ActionCreate},
		{"POST", "/files", "", "form", access.ActionCreate},
	} {
		target, err := resolveReplayTarget(httptest.NewRequest(tc.method, tc.path, nil))
		if err != nil || target.key != tc.key || target.kind != tc.kind || target.actions[0] != tc.action {
			t.Errorf("%s %s: %+v %v", tc.method, tc.path, target, err)
		}
	}
	for _, request := range [][2]string{{"PATCH", "/files/a"}, {"POST", "/files/a/tags"}, {"POST", "/files/a/presign?op=delete"}, {"GET", "/files/a"}, {"PUT", "/other/a"}} {
		if _, err := resolveReplayTarget(httptest.NewRequest(request[0], request[1], nil)); !access.IsResourceBoundaryFault(err) {
			t.Errorf("unsupported replay route accepted: %v %v", request, err)
		}
	}
}

func loadReplayRecord(t *testing.T, f *replayFixture, key string) repository.IdempotencyRecord {
	t.Helper()
	record, claimed, err := f.raw.ClaimIdempotencyKey(t.Context(), "default", key, "", "")
	if err != nil || claimed || record.Status != repository.IdempotencyCompleted {
		t.Fatalf("persisted record: %+v claimed=%v err=%v", record, claimed, err)
	}
	return record
}

func cloneReplayRecord(t *testing.T, record repository.IdempotencyRecord) repository.IdempotencyRecord {
	t.Helper()
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var clone repository.IdempotencyRecord
	if err := json.Unmarshal(body, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func alterReplayAdmission(t *testing.T, record *repository.IdempotencyRecord, mutate func(*service.ReplayAdmission)) {
	t.Helper()
	admission, err := cachedReplayAdmission(*record)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&admission)
	record.ResponseHeaders = replayAdmissionHeaders(record.ResponseHeaders, &admission)
}

func replayCorruptions(t *testing.T) map[string]func(*repository.IdempotencyRecord) {
	return map[string]func(*repository.IdempotencyRecord){
		"record tenant":      func(r *repository.IdempotencyRecord) { r.TenantID = "other" },
		"record key":         func(r *repository.IdempotencyRecord) { r.Key = "other" },
		"wrong status":       func(r *repository.IdempotencyRecord) { r.ResponseStatus = 204 },
		"wrong content type": func(r *repository.IdempotencyRecord) { r.ResponseCT = "application/octet-stream" },
		"null DTO":           func(r *repository.IdempotencyRecord) { r.ResponseBody = []byte("null") },
		"wrong DTO key":      func(r *repository.IdempotencyRecord) { r.ResponseBody = []byte(`{"bucket":"default","key":"another"}`) },
		"duplicate DTO identity": func(r *repository.IdempotencyRecord) {
			r.ResponseBody = []byte(`{"bucket":"other","bucket":"default","key":"doc"}`)
		},
		"trailing DTO":              func(r *repository.IdempotencyRecord) { r.ResponseBody = append(r.ResponseBody, []byte(` {}`)...) },
		"missing original identity": func(r *repository.IdempotencyRecord) { delete(r.ResponseHeaders, replayAdmissionHeader) },
		"bad original identity": func(r *repository.IdempotencyRecord) {
			r.ResponseHeaders[replayAdmissionHeader] = []string{`{"version":1,"admission":null}`}
		},
		"lowercase ETag":        func(r *repository.IdempotencyRecord) { r.ResponseHeaders["etag"] = []string{`"another"`} },
		"contradictory version": func(r *repository.IdempotencyRecord) { r.ResponseHeaders["x-version-id"] = []string{"another"} },
		"contradictory Location": func(r *repository.IdempotencyRecord) {
			r.ResponseHeaders["Location"] = []string{"https://aero.test/v1/files/another"}
		},
		"object ID": func(r *repository.IdempotencyRecord) {
			alterReplayAdmission(t, r, func(a *service.ReplayAdmission) { a.Object.ID = 0 })
		},
		"object version": func(r *repository.IdempotencyRecord) {
			alterReplayAdmission(t, r, func(a *service.ReplayAdmission) { a.Object.VersionID = "" })
		},
		"resource bucket": func(r *repository.IdempotencyRecord) {
			alterReplayAdmission(t, r, func(a *service.ReplayAdmission) { a.Resource.Bucket = "other" })
		},
		"resource owner": func(r *repository.IdempotencyRecord) {
			alterReplayAdmission(t, r, func(a *service.ReplayAdmission) { a.Resource.OwnerID = "forged" })
		},
		"action": func(r *repository.IdempotencyRecord) {
			alterReplayAdmission(t, r, func(a *service.ReplayAdmission) { a.Action = access.ActionAll })
		},
		"missing object": func(r *repository.IdempotencyRecord) {
			alterReplayAdmission(t, r, func(a *service.ReplayAdmission) { a.Object = nil })
		},
	}
}

func TestIdempotencyBoundaryRejectsContradictoryPersistedRecords(t *testing.T) {
	f := newReplayFixture(t)
	first := f.request("PUT", "/files/doc", "record", strings.NewReader("original"), "text/plain")
	if first.Code != 201 {
		t.Fatalf("fresh: %d %s", first.Code, first.Body)
	}
	original := loadReplayRecord(t, f, "record")
	h := NewHandler(f.svc, idemSilentLogger()).WithResourceReaders(f.readers)
	r := httptest.NewRequest("PUT", "/files/doc", strings.NewReader("retry cannot repair"))
	r.Header.Set("Idempotency-Key", "record")
	before := f.effects()
	for name, corrupt := range replayCorruptions(t) {
		t.Run(name, func(t *testing.T) {
			record := cloneReplayRecord(t, original)
			corrupt(&record)
			if err := h.AdmitReplay(r, record); !access.IsResourceBoundaryFault(err) {
				t.Fatalf("contradiction admitted or flattened: %v", err)
			}
		})
	}
	if f.effects() != before {
		t.Fatal("cached response validation did business work")
	}
}

func TestIdempotencyBoundaryMissingAndTypedNilCannotReplay(t *testing.T) {
	f := newReplayFixture(t)
	var typedNil *Handler
	for i, boundaries := range [][]IdempotencyReplayBoundary{nil, {typedNil}} {
		calls := 0
		handler := idempotency(f.raw, idemSilentLogger(), false, boundaries...)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(201) }))
		request := func() *httptest.ResponseRecorder {
			r := httptest.NewRequest("PUT", "/files/doc", nil)
			r.Header.Set("Idempotency-Key", []string{"missing", "typed-nil"}[i])
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			return w
		}
		if first := request(); first.Code != 201 {
			t.Fatalf("fresh: %d", first.Code)
		}
		if cached := request(); cached.Code != 500 || cached.Header().Get("Idempotency-Replayed") != "" {
			t.Fatalf("missing boundary replayed: %d %s", cached.Code, cached.Body)
		}
		if calls != 1 {
			t.Fatalf("missing admission re-executed: %d", calls)
		}
	}
}

func TestIdempotencyBoundaryDeadlineRetains504(t *testing.T) {
	f := newReplayFixture(t)
	if first := f.request("PUT", "/files/doc", "deadline", strings.NewReader("original"), "text/plain"); first.Code != 201 {
		t.Fatal(first.Body.String())
	}
	readers := f.readers
	readers.Binding = sseBindingFunc(func(context.Context, string, string) (spaces.BindingResult, error) {
		return spaces.BindingResult{}, errors.Join(context.DeadlineExceeded, repository.ErrNotFound, access.ErrDenied)
	})
	f.router = NewRouter(f.svc, f.repo, nil, nil, nil, nil, nil, idemSilentLogger(), false, nil, nil, 0, false, func(h *Handler) { h.WithResourceReaders(readers) })
	before := f.effects()
	if cached := f.request("PUT", "/files/doc", "deadline", nil, ""); cached.Code != 504 || cached.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("deadline flattened: %d %s", cached.Code, cached.Body)
	}
	if f.effects() != before {
		t.Fatal("deadline repeated effects")
	}
}

func changeCachedURL(t *testing.T, dto *presignResponse, mutate func(*url.URL, url.Values)) {
	t.Helper()
	u, err := url.Parse(dto.URL)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	mutate(u, query)
	u.RawQuery = query.Encode()
	dto.URL = u.String()
}

func TestIdempotencyBoundaryCachedPresignIdentityAndSignature(t *testing.T) {
	f := newReplayFixture(t)
	if _, err := f.svc.Put(t.Context(), "default", "default", "doc", strings.NewReader("x"), 1, service.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	path := "/files/doc/presign?op=get&expires=300"
	if first := f.request("POST", path, "presign", nil, ""); first.Code != 200 {
		t.Fatal(first.Body.String())
	}
	record := loadReplayRecord(t, f, "presign")
	before := f.effects()
	for _, mutate := range []func(*presignResponse){
		func(dto *presignResponse) {
			changeCachedURL(t, dto, func(u *url.URL, _ url.Values) { u.Path = "/v1/files/other" })
		},
		func(dto *presignResponse) {
			changeCachedURL(t, dto, func(u *url.URL, _ url.Values) { u.Host = "other.test" })
		},
		func(dto *presignResponse) {
			changeCachedURL(t, dto, func(_ *url.URL, q url.Values) { q.Set("x-aero-presign-tenant", "other") })
		},
		func(dto *presignResponse) {
			changeCachedURL(t, dto, func(_ *url.URL, q url.Values) { q.Add("x-aero-presign-operation", "get") })
		},
		func(dto *presignResponse) {
			changeCachedURL(t, dto, func(_ *url.URL, q url.Values) { q.Set("x-aero-presign-signature", "forged") })
		},
		func(dto *presignResponse) { dto.Expires = dto.Expires.Add(time.Hour) },
	} {
		var dto presignResponse
		if err := json.Unmarshal(record.ResponseBody, &dto); err != nil {
			t.Fatal(err)
		}
		mutate(&dto)
		body, _ := json.Marshal(dto)
		if err := f.raw.CompleteIdempotencyKey(t.Context(), "default", record.Key, record.ResponseStatus, body, record.ResponseCT, record.ResponseHeaders); err != nil {
			t.Fatal(err)
		}
		if cached := f.request("POST", path, "presign", nil, ""); cached.Code != 500 || cached.Header().Get("Idempotency-Replayed") != "" {
			t.Fatalf("contradictory presign replayed: %d %s", cached.Code, cached.Body)
		}
	}
	if f.effects() != before {
		t.Fatal("invalid cached presign renewed or performed effects")
	}
}

func TestRouterResourceReaderDiscoveryAndExplicitPreflight(t *testing.T) {
	f := newReplayFixture(t)
	if err := ValidateRouterResourceReaders(DiscoverRouterResourceReaders(f.raw)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRouterResourceReaders(DiscoverRouterResourceReaders(f.repo)); !access.IsResourceBoundaryFault(err) {
		t.Fatalf("wrapped ports invented: %v", err)
	}
	readers := f.readers
	readers.EventAuthority = nil
	if err := ValidateRouterResourceReaders(readers); !access.IsResourceBoundaryFault(err) {
		t.Fatalf("missing authority preflight: %v", err)
	}
}

func TestIdempotencyBoundaryExplicitMissingResourceDoesNotRediscover(t *testing.T) {
	f := newReplayFixture(t)
	if first := f.request("PUT", "/files/doc", "record", strings.NewReader("original"), "text/plain"); first.Code != 201 {
		t.Fatal(first.Body.String())
	}
	record := loadReplayRecord(t, f, "record")
	readers := f.readers
	readers.Resources = nil
	h := NewHandler(f.svc, idemSilentLogger()).WithResourceReaders(readers)
	r := httptest.NewRequest("PUT", "/files/doc", nil)
	r.Header.Set("Idempotency-Key", "record")
	if err := h.AdmitReplay(r, record); !access.IsResourceBoundaryFault(err) {
		t.Fatalf("missing raw resource silently rediscovered: %v", err)
	}
}
