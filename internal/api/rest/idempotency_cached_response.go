package rest

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// This server-only metadata lives in the existing persisted headers column. It
// is never sent on the original response or replay. No schema or body changes.
const replayAdmissionHeader = "Aero-Internal-Replay-Admission"

type cachedAdmission struct {
	Version   int                     `json:"version"`
	Admission service.ReplayAdmission `json:"admission"`
}

func replayAdmissionHeaders(headers map[string][]string, admission *service.ReplayAdmission) map[string][]string {
	if headers == nil {
		headers = make(map[string][]string)
	}
	for key := range headers {
		if strings.EqualFold(key, replayAdmissionHeader) {
			delete(headers, key)
		}
	}
	if admission != nil && admission.Resource.Key != "" {
		body, err := json.Marshal(cachedAdmission{Version: 1, Admission: *admission})
		if err == nil {
			headers[replayAdmissionHeader] = []string{string(body)}
		}
	}
	return headers
}

func cachedReplayAdmission(rec repository.IdempotencyRecord) (service.ReplayAdmission, error) {
	var encoded []string
	seen := false
	for key, values := range rec.ResponseHeaders {
		if strings.EqualFold(key, replayAdmissionHeader) {
			if seen {
				return service.ReplayAdmission{}, replayFault(errors.New("duplicate replay admission"))
			}
			encoded, seen = values, true
		}
	}
	if len(encoded) != 1 {
		return service.ReplayAdmission{}, replayFault(errors.New("missing original replay admission"))
	}
	var cached cachedAdmission
	if err := decodeCachedJSON([]byte(encoded[0]), &cached); err != nil {
		return service.ReplayAdmission{}, err
	}
	if cached.Version != 1 || cached.Admission.Resource.TenantID != rec.TenantID {
		return service.ReplayAdmission{}, replayFault(errors.New("invalid replay admission version or tenant"))
	}
	return cached.Admission, nil
}

func validateCachedTarget(r *http.Request, rec repository.IdempotencyRecord, target replayTarget, admission service.ReplayAdmission) error {
	if err := service.ValidateReplayAdmission(admission); err != nil {
		return err
	}
	if err := validateCachedScope(r, target, admission.Resource); err != nil {
		return err
	}
	if err := validateCachedAction(target, admission); err != nil {
		return err
	}
	if err := validateCachedHeaders(rec, admission); err != nil {
		return err
	}
	if err := validateCachedContentType(rec, target); err != nil {
		return err
	}
	return validateCachedBody(r, rec, target, admission)
}

func validateCachedScope(r *http.Request, target replayTarget, resource access.Resource) error {
	if resource.TenantID != mw.TenantFrom(r.Context()) || resource.Bucket != service.DefaultBucket || resource.Key == "" {
		return replayFault(errors.New("cached resource scope conflict"))
	}
	if target.kind != "form" && resource.Key != target.key {
		return replayFault(errors.New("cached response target conflict"))
	}
	return nil
}

func validateCachedAction(target replayTarget, admission service.ReplayAdmission) error {
	allowed := false
	for _, action := range target.actions {
		allowed = allowed || action == admission.Action
	}
	if !allowed || admission.Action == access.ActionAll {
		return replayFault(errors.New("cached response action conflict"))
	}
	if admission.Object == nil && !(target.kind == "presign" && admission.Action == access.ActionCreate) {
		return replayFault(errors.New("missing original object identity"))
	}
	return nil
}

func validateCachedContentType(rec repository.IdempotencyRecord, target replayTarget) error {
	if target.kind == "empty" {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(rec.ResponseCT)
	if err != nil || mediaType != "application/json" {
		return replayFault(errors.New("cached response content type conflict"))
	}
	return nil
}

func validateCachedHeaders(rec repository.IdempotencyRecord, admission service.ReplayAdmission) error {
	version, etag := "", ""
	if admission.Object != nil {
		version, etag = admission.Object.VersionID, admission.Object.ETag
	}
	for _, name := range []string{"X-Version-Id", "X-Amz-Version-Id"} {
		if err := validateCachedIdentityHeader(rec, name, version, admission.Object != nil); err != nil {
			return err
		}
	}
	if err := validateCachedIdentityHeader(rec, "ETag", etag, admission.Object != nil); err != nil {
		return err
	}
	return validateCachedLocation(cachedHeaderValues(rec, "Location"), admission.Resource.Key)
}

func validateCachedIdentityHeader(rec repository.IdempotencyRecord, name, want string, hasObject bool) error {
	values := cachedHeaderValues(rec, name)
	if len(values) == 0 {
		return nil
	}
	if !hasObject || len(values) != 1 {
		return replayFault(errors.New("cached identity header conflict"))
	}
	value := values[0]
	if name == "ETag" && value == `"`+want+`"` {
		value = want
	}
	if value != want {
		return replayFault(errors.New("cached identity header conflict"))
	}
	return nil
}

func validateCachedLocation(values []string, key string) error {
	if len(values) == 0 {
		return nil
	}
	if len(values) != 1 {
		return replayFault(errors.New("ambiguous cached Location"))
	}
	u, err := url.Parse(values[0])
	if err != nil || u.Path != "/v1/files/"+key || u.RawQuery != "" || u.Fragment != "" || u.IsAbs() || u.Host != "" {
		return replayFault(errors.New("cached Location conflict"))
	}
	return nil
}

func cachedHeaderValues(rec repository.IdempotencyRecord, name string) []string {
	var values []string
	for key, entries := range rec.ResponseHeaders {
		if strings.EqualFold(key, name) {
			values = append(values, entries...)
		}
	}
	return values
}

func validateCachedBody(r *http.Request, rec repository.IdempotencyRecord, target replayTarget, admission service.ReplayAdmission) error {
	if rec.ResponseStatus != cachedResponseStatus(target.kind) {
		return replayFault(errors.New("cached response status conflict"))
	}
	switch target.kind {
	case "object", "form":
		var dto objectDTO
		if err := decodeCachedJSON(rec.ResponseBody, &dto); err != nil {
			return err
		}
		if admission.Object == nil || !reflect.DeepEqual(dto, toObjectDTO(*admission.Object)) {
			return replayFault(errors.New("cached object DTO identity conflict"))
		}
	case "empty":
		if len(rec.ResponseBody) != 0 {
			return replayFault(errors.New("cached empty response has a body"))
		}
	case "presign":
		return validateCachedPresign(r, rec, admission)
	default:
		return validateCachedMutationBody(rec.ResponseBody, target.kind)
	}
	return nil
}

func cachedResponseStatus(kind string) int {
	switch kind {
	case "object", "form":
		return http.StatusCreated
	case "empty":
		return http.StatusNoContent
	default:
		return http.StatusOK
	}
}

func validateCachedMutationBody(body []byte, kind string) error {
	switch kind {
	case "tags":
		return validateCachedTags(body)
	case "acl":
		return validateCachedACL(body)
	case "metadata", "restore":
		return validateCachedMutationStatus(body, kind)
	case "lock":
		return validateCachedLock(body)
	default:
		return replayFault(errors.New("unknown cached response class"))
	}
}

func validateCachedTags(body []byte) error {
	var dto struct {
		Tags map[string]string `json:"tags"`
	}
	if err := decodeCachedJSON(body, &dto); err != nil {
		return err
	}
	if dto.Tags == nil {
		return replayFault(errors.New("missing cached tags"))
	}
	return nil
}

func validateCachedACL(body []byte) error {
	var dto aclBody
	if err := decodeCachedJSON(body, &dto); err != nil {
		return err
	}
	if dto.ACL == "" || service.ValidateACL(dto.ACL) != nil {
		return replayFault(errors.New("invalid cached ACL"))
	}
	return nil
}

func validateCachedMutationStatus(body []byte, kind string) error {
	var dto struct {
		Status string `json:"status"`
	}
	if err := decodeCachedJSON(body, &dto); err != nil {
		return err
	}
	want := "ok"
	if kind == "restore" {
		want = "restored"
	}
	if dto.Status != want {
		return replayFault(errors.New("invalid cached mutation status"))
	}
	return nil
}

func validateCachedLock(body []byte) error {
	var dto struct {
		Until string `json:"locked_until"`
	}
	if err := decodeCachedJSON(body, &dto); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339, dto.Until); err != nil {
		return replayFault(err)
	}
	return nil
}

func validateCachedPresign(r *http.Request, rec repository.IdempotencyRecord, admission service.ReplayAdmission) error {
	var dto presignResponse
	if err := decodeCachedJSON(rec.ResponseBody, &dto); err != nil {
		return err
	}
	u, err := url.Parse(dto.URL)
	if err != nil {
		return replayFault(err)
	}
	if err := validateCachedPresignTarget(u, admission.Resource.Key); err != nil {
		return err
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return replayFault(err)
	}
	expiry, err := strconv.ParseInt(q.Get("x-aero-presign-expires"), 10, 64)
	if err != nil || q.Get("x-aero-presign-tenant") != admission.Resource.TenantID || q.Get("x-aero-presign-signature") == "" {
		return replayFault(errors.New("cached presign scope conflict"))
	}
	if err := validateCachedPresignOperation(r.URL.Query().Get("op"), q.Get("x-aero-presign-operation")); err != nil {
		return err
	}
	return validateCachedPresignExpiry(dto.Expires, expiry) // original bytes only; no signing
}

func validateCachedPresignTarget(u *url.URL, key string) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return replayFault(errors.New("cached presign URL scheme conflict"))
	}
	if u.Host == "" || u.User != nil || u.Fragment != "" || u.Path != "/v1/files/"+key {
		return replayFault(errors.New("cached presign URL identity conflict"))
	}
	return nil
}

func validateCachedPresignOperation(operation, got string) error {
	switch operation {
	case "", "get":
		if got != "get" {
			return replayFault(errors.New("cached presign operation conflict"))
		}
	case "put":
		if got != "" && got != "put" {
			return replayFault(errors.New("cached presign operation conflict"))
		}
	default:
		return replayFault(errors.New("invalid presign operation"))
	}
	return nil
}

func validateCachedPresignExpiry(expires time.Time, signed int64) error {
	if expires.IsZero() || expires.Unix() < signed || expires.Unix() > signed+1 {
		return replayFault(errors.New("cached presign expiry conflict"))
	}
	return nil
}

func decodeCachedJSON(body []byte, out any) error {
	if err := validateCachedJSONValue(json.NewDecoder(bytes.NewReader(body))); err != nil {
		return replayFault(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return replayFault(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return replayFault(errors.New("trailing cached response JSON"))
	}
	return nil
}

// Go's ordinary decoder accepts duplicate members (last value wins). Cached
// authority must not silently choose one of two contradictory persisted facts.
func validateCachedJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	if delimiter == '{' {
		return validateCachedJSONObject(decoder)
	}
	if delimiter != '[' {
		return errors.New("invalid cached JSON delimiter")
	}
	for decoder.More() {
		if err := validateCachedJSONValue(decoder); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func validateCachedJSONObject(decoder *json.Decoder) error {
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return errors.New("duplicate or invalid cached JSON member")
		}
		seen[key] = true
		if err := validateCachedJSONValue(decoder); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
}
