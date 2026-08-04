package s3compat

import (
	"encoding/xml"
	"net/http"
	"testing"
	"time"
)

func TestObjectRetentionRoundTripAndDeleteProtection(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/locked/object.txt"
	resp, _ := do(t, http.MethodPut, base, []byte("protected"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d", resp.StatusCode)
	}
	until := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	body, _ := xml.Marshal(objectRetention{
		Mode: "COMPLIANCE", RetainUntilDate: until.Format(time.RFC3339Nano),
	})
	resp, result := do(t, http.MethodPut, base+"?retention", body, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put retention status=%d body=%s", resp.StatusCode, result)
	}
	resp, result = do(t, http.MethodGet, base+"?retention", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get retention status=%d body=%s", resp.StatusCode, result)
	}
	var got objectRetention
	if err := xml.Unmarshal(result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "COMPLIANCE" || got.RetainUntilDate != until.Format(time.RFC3339Nano) {
		t.Fatalf("retention roundtrip = %+v", got)
	}
	resp, _ = do(t, http.MethodDelete, base, nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("delete retained object status = %d, want 403", resp.StatusCode)
	}
}

func TestObjectRetentionRejectsMalformedAndPastDate(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL + "/locked/invalid.txt"
	do(t, http.MethodPut, base, []byte("x"), nil)
	resp, _ := do(t, http.MethodPut, base+"?retention", []byte("<Retention>"), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed retention status = %d", resp.StatusCode)
	}
	body, _ := xml.Marshal(objectRetention{
		Mode: "GOVERNANCE", RetainUntilDate: time.Now().Add(-time.Hour).Format(time.RFC3339Nano),
	})
	resp, _ = do(t, http.MethodPut, base+"?retention", body, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("past retention status = %d", resp.StatusCode)
	}
}

func TestObjectLegalHoldXMLTargetsSubresource(t *testing.T) {
	srv := newTestServer(t)
	url := srv.URL + "/locked/held.txt"
	do(t, http.MethodPut, url, []byte("held"), nil)
	body, _ := xml.Marshal(objectLegalHold{Status: "ON"})
	resp, result := do(t, http.MethodPut, url+"?legal-hold", body, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put legal hold status=%d body=%s", resp.StatusCode, result)
	}
	resp, result = do(t, http.MethodGet, url+"?legal-hold", nil, nil)
	var hold objectLegalHold
	if err := xml.Unmarshal(result, &hold); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || hold.Status != "ON" {
		t.Fatalf("get legal hold status=%d hold=%q body=%s", resp.StatusCode, hold.Status, result)
	}
	resp, _ = do(t, http.MethodDelete, url, nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("delete held object status=%d, want 403", resp.StatusCode)
	}
	body, _ = xml.Marshal(objectLegalHold{Status: "OFF"})
	resp, result = do(t, http.MethodPut, url+"?legal-hold", body, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remove legal hold status=%d body=%s", resp.StatusCode, result)
	}
}

func TestPutObjectLegalHoldHeaderRoundTripAndRelease(t *testing.T) {
	srv := newTestServer(t)
	url := srv.URL + "/locked/header-held.txt"
	resp, result := do(t, http.MethodPut, url, []byte("held"), map[string]string{
		"x-amz-object-lock-legal-hold": "ON",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put held object status=%d body=%s", resp.StatusCode, result)
	}
	resp, result = do(t, http.MethodGet, url+"?legal-hold", nil, nil)
	var hold objectLegalHold
	if err := xml.Unmarshal(result, &hold); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || hold.Status != "ON" {
		t.Fatalf("header legal hold status=%d hold=%q body=%s", resp.StatusCode, hold.Status, result)
	}
	resp, _ = do(t, http.MethodDelete, url, nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("held delete status=%d, want 403", resp.StatusCode)
	}

	offBody, _ := xml.Marshal(objectLegalHold{Status: "OFF"})
	resp, result = do(t, http.MethodPut, url+"?legal-hold", offBody, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("release header legal hold status=%d body=%s", resp.StatusCode, result)
	}
	resp, result = do(t, http.MethodDelete, url, nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete after release status=%d body=%s", resp.StatusCode, result)
	}
}

func TestPutObjectRejectsInvalidLegalHoldHeaderBeforeWrite(t *testing.T) {
	srv := newTestServer(t)
	url := srv.URL + "/locked/invalid-header.txt"
	resp, result := do(t, http.MethodPut, url, []byte("body"), map[string]string{
		"x-amz-object-lock-legal-hold": "MAYBE",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid legal hold status=%d body=%s", resp.StatusCode, result)
	}
	resp, _ = do(t, http.MethodGet, url, nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("invalid legal hold still wrote object: status=%d", resp.StatusCode)
	}
}
