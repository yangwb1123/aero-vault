package rest

import (
	"bytes"
	"errors"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

type replayMutationCase struct {
	name, method, path, body string
	action                   access.Action
	status                   int
	seed, restore, form      bool
}

func replayMutationCases() []replayMutationCase {
	return []replayMutationCase{
		{name: "raw", method: "PUT", path: "/files/doc", body: "first bytes", action: access.ActionWrite, status: 201, seed: true},
		{name: "create", method: "PUT", path: "/files/doc", body: "first bytes", action: access.ActionCreate, status: 201},
		{name: "form", method: "POST", path: "/files", action: access.ActionCreate, status: 201, form: true},
		{name: "lock", method: "POST", path: "/files/doc/lock", body: `{"seconds":600}`, action: access.ActionWrite, status: 200, seed: true},
		{name: "restore", method: "POST", path: "/files/doc/restore", action: access.ActionRestore, status: 200, seed: true, restore: true},
		{name: "put tags", method: "PUT", path: "/files/doc/tags", body: `{"tag":"original"}`, action: access.ActionWrite, status: 200, seed: true},
		{name: "put acl", method: "PUT", path: "/files/doc/acl", body: `{"acl":"public-read"}`, action: access.ActionManageACL, status: 200, seed: true},
		{name: "put metadata", method: "PUT", path: "/files/doc/metadata", body: `{"note":"original"}`, action: access.ActionWrite, status: 200, seed: true},
		{name: "patch", method: "PATCH", path: "/files/doc/metadata", body: `{"note":"original"}`, action: access.ActionWrite, status: 200, seed: true},
		{name: "delete tags", method: "DELETE", path: "/files/doc/tags", action: access.ActionWrite, status: 204, seed: true},
		{name: "delete metadata key", method: "DELETE", path: "/files/doc/metadata?key=note", action: access.ActionWrite, status: 200, seed: true},
		{name: "delete metadata all", method: "DELETE", path: "/files/doc/metadata", action: access.ActionWrite, status: 200, seed: true},
		{name: "delete soft", method: "DELETE", path: "/files/doc", action: access.ActionDelete, status: 204, seed: true},
		{name: "delete hard", method: "DELETE", path: "/files/doc?hard=1", action: access.ActionDelete, status: 204, seed: true},
		{name: "presign get", method: "POST", path: "/files/doc/presign?op=get&expires=300", action: access.ActionDownload, status: 200, seed: true},
		{name: "presign put", method: "POST", path: "/files/doc/presign?op=put&expires=300", action: access.ActionCreate, status: 200},
	}
}

func formReplayBody(t *testing.T, key string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("key", key); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "upload.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("original file")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.FormDataContentType()
}

func requestReplayCase(t *testing.T, f *replayFixture, tc replayMutationCase, retry bool) *httpResponseSnapshot {
	t.Helper()
	body, contentType := strings.NewReader(tc.body), "application/json"
	if tc.form {
		key := "doc"
		if retry {
			key = "other"
		}
		form, ct := formReplayBody(t, key)
		w := f.request(tc.method, tc.path, "mutation", form, ct)
		return &httpResponseSnapshot{status: w.Code, body: w.Body.String(), headers: w.Header()}
	}
	w := f.request(tc.method, tc.path, "mutation", body, contentType)
	return &httpResponseSnapshot{status: w.Code, body: w.Body.String(), headers: w.Header()}
}

type httpResponseSnapshot struct {
	status  int
	body    string
	headers http.Header
}

func prepareReplayCase(t *testing.T, f *replayFixture, tc replayMutationCase) *replayActionProvider {
	t.Helper()
	if tc.seed {
		if _, err := f.svc.Put(t.Context(), "default", "default", "doc", strings.NewReader("seed"), 4, service.PutOptions{Metadata: map[string]string{"note": "seed"}}); err != nil {
			t.Fatal(err)
		}
	}
	if tc.restore {
		f.svc.WithDeleteFailOpen(true)
		if err := f.svc.Delete(t.Context(), "default", "default", "doc", false); err != nil {
			t.Fatal(err)
		}
	}
	provider := &replayActionProvider{action: tc.action, key: "doc"}
	f.svc.WithAuthorizer(provider)
	return provider
}

func assertReplayRefused(t *testing.T, response *httpResponseSnapshot, status int) {
	t.Helper()
	if response.status != status || response.headers.Get("Idempotency-Replayed") != "" {
		t.Fatalf("refused replay: status=%d headers=%v body=%s", response.status, response.headers, response.body)
	}
	for _, private := range []string{"first bytes", "original file", "private persistence fault", "provider fault", replayAdmissionHeader} {
		if strings.Contains(response.body, private) {
			t.Fatalf("refusal leaked %q: %s", private, response.body)
		}
	}
}

func runReplayMutationCase(t *testing.T, tc replayMutationCase) {
	t.Helper()
	f := newReplayFixture(t)
	provider := prepareReplayCase(t, f, tc)
	first := requestReplayCase(t, f, tc, false)
	if first.status != tc.status {
		t.Fatalf("fresh mutation: %d %s", first.status, first.body)
	}
	before := f.effects()
	if tc.action == access.ActionCreate && !strings.Contains(tc.path, "/presign") {
		provider = &replayActionProvider{action: access.ActionWrite, key: "doc"}
		f.svc.WithAuthorizer(provider)
	}
	provider.deny.Store(true)
	assertReplayRefused(t, requestReplayCase(t, f, tc, true), http.StatusForbidden)
	provider.deny.Store(false)
	provider.fault.Store(true)
	assertReplayRefused(t, requestReplayCase(t, f, tc, true), http.StatusInternalServerError)
	provider.fault.Store(false)
	f.gate.mode.Store(1)
	assertReplayRefused(t, requestReplayCase(t, f, tc, true), http.StatusForbidden)
	f.gate.mode.Store(2)
	assertReplayRefused(t, requestReplayCase(t, f, tc, true), http.StatusInternalServerError)
	f.gate.mode.Store(0)
	cached := requestReplayCase(t, f, tc, true)
	assertReplayMatches(t, first, cached)
	if f.effects() != before {
		t.Fatalf("cache hit re-executed mutation: %v -> %v", before, f.effects())
	}
	if tc.form {
		if _, err := f.raw.GetObject(t.Context(), "default", "default", "other"); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("retry body repaired form identity: %v", err)
		}
	}
}

func assertReplayMatches(t *testing.T, first, cached *httpResponseSnapshot) {
	t.Helper()
	if cached.status != first.status || cached.body != first.body || cached.headers.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("legal replay changed bytes/expiry: first=%+v cached=%+v", first, cached)
	}
	if cached.headers.Get(replayAdmissionHeader) != "" || first.headers.Get(replayAdmissionHeader) != "" {
		t.Fatal("internal replay facts reached client headers")
	}
}

func TestSpaceBoundaryActualRouterReplayMatrix(t *testing.T) {
	for _, tc := range replayMutationCases() {
		t.Run(tc.name, func(t *testing.T) { runReplayMutationCase(t, tc) })
	}
}

func TestSpaceBoundaryReplayUsesCurrentBucketPolicy(t *testing.T) {
	f := newReplayFixture(t)
	if first := f.request("PUT", "/files/doc", "policy", strings.NewReader("original"), "text/plain"); first.Code != 201 {
		t.Fatal(first.Body.String())
	}
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"s3:PutObject","Resource":"arn:aws:s3:::default/doc"}]}`
	if err := f.raw.SetBucketPolicy(t.Context(), "default", "default", policy); err != nil {
		t.Fatal(err)
	}
	before := f.effects()
	if cached := f.request("PUT", "/files/doc", "policy", nil, ""); cached.Code != 403 || cached.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("current bucket policy bypassed: %d %s", cached.Code, cached.Body)
	}
	if f.effects() != before {
		t.Fatal("policy rejection repeated effects")
	}
}

func TestSpaceBoundaryReplayDoesNotRepairFromLatestObject(t *testing.T) {
	f := newReplayFixture(t)
	first := f.request("PUT", "/files/doc", "original", strings.NewReader("old"), "text/plain")
	if first.Code != 201 {
		t.Fatalf("fresh: %d %s", first.Code, first.Body)
	}
	if _, err := f.svc.Put(t.Context(), "default", "default", "doc", strings.NewReader("replacement"), 11, service.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	before := f.effects()
	cached := f.request("PUT", "/files/doc", "original", strings.NewReader("retry body"), "text/plain")
	if cached.Code != 500 || cached.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("latest replacement used as identity proof: %d %s", cached.Code, cached.Body)
	}
	if f.effects() != before {
		t.Fatal("rejected replaced identity re-executed")
	}
}

func TestSpaceBoundaryReplayDeleteStillNeedsProvider(t *testing.T) {
	f := newReplayFixture(t)
	tc := replayMutationCase{method: "DELETE", path: "/files/doc?hard=1", action: access.ActionDelete, seed: true}
	prepareReplayCase(t, f, tc)
	if first := f.request(tc.method, tc.path, "delete", nil, ""); first.Code != 204 {
		t.Fatalf("fresh delete: %d %s", first.Code, first.Body)
	}
	bare := service.NewFileService(f.store, f.repo, nil).WithSpaceBoundary(service.NewSpaceBoundary(f.readers.SpaceBoundaryReaders))
	f.router = NewRouter(bare, f.repo, nil, nil, nil, nil, nil, idemSilentLogger(), false, nil, nil, 0, false, func(h *Handler) { h.WithResourceReaders(f.readers) })
	before := f.effects()
	if cached := f.request(tc.method, tc.path, "delete", nil, ""); cached.Code != 403 || cached.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("unbound admission replaced delete provider: %d %s", cached.Code, cached.Body)
	}
	if f.effects() != before {
		t.Fatal("missing delete provider repeated effects")
	}
}
