package s3compat

import (
	"encoding/xml"
	"errors"
	"net/http"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

func writeS3Error(w http.ResponseWriter, r *http.Request, err error) {
	code, msg, status := classify(err)
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	body := s3Error{
		Code:      code,
		Message:   msg,
		Resource:  r.URL.Path,
		RequestID: mw.RequestIDFrom(r.Context()),
	}
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(body)
}

// errNoSuchLifecycle signals that a bucket has no lifecycle configuration, which
// AWS reports as a 404 NoSuchLifecycleConfiguration rather than an empty body.
var errNoSuchLifecycle = errors.New("the lifecycle configuration does not exist")

// errNoSuchBucketPolicy signals that a bucket has no policy, which AWS reports
// as a 404 NoSuchBucketPolicy rather than an empty body.
var errNoSuchBucketPolicy = errors.New("the bucket policy does not exist")
var errNoSuchWebsite = errors.New("the website configuration does not exist")
var errNoSuchTagSet = errors.New("the tag set does not exist")

// errMalformedXML signals an unparsable request body (400 MalformedXML).
var errMalformedXML = errors.New("the XML you provided was not well-formed or did not validate")

// errNoSuchBucket signals a request against a bucket that does not exist
// (404 NoSuchBucket).
var errNoSuchBucket = errors.New("the specified bucket does not exist")
var errBucketNotEmpty = errors.New("the bucket you tried to delete is not empty")

var s3CodeStatus = map[string]int{
	"MalformedJSON":                http.StatusBadRequest,
	"EntityTooLarge":               http.StatusRequestEntityTooLarge,
	"MalformedXML":                 http.StatusBadRequest,
	"NoSuchBucket":                 http.StatusNotFound,
	"BucketNotEmpty":               http.StatusConflict,
	"NoSuchBucketPolicy":           http.StatusNotFound,
	"NoSuchLifecycleConfiguration": http.StatusNotFound,
	"NoSuchWebsiteConfiguration":   http.StatusNotFound,
	"NoSuchTagSet":                 http.StatusNotFound,
	"NoSuchKey":                    http.StatusNotFound,
	"InvalidRange":                 http.StatusRequestedRangeNotSatisfiable,
	"PreconditionFailed":           http.StatusPreconditionFailed,
	"NoSuchUpload":                 http.StatusNotFound,
	"InvalidArgument":              http.StatusBadRequest,
	"BadDigest":                    http.StatusBadRequest,
	"IncompleteBody":               http.StatusBadRequest,
	"AccessDenied":                 http.StatusForbidden,
	"AccessDenied.Locked":          http.StatusForbidden,
	"ObjectCorrupt":                http.StatusGone,
	"QuotaExceeded":                http.StatusForbidden,
	"ServiceUnavailable":           http.StatusServiceUnavailable,
	"NotImplemented":               http.StatusNotImplemented,
}

var s3CodeMessage = map[string]string{
	"MalformedXML":                 "The XML you provided was not well-formed or did not validate against our published schema.",
	"EntityTooLarge":               "Your proposed upload exceeds the maximum allowed object size.",
	"NoSuchBucket":                 "The specified bucket does not exist.",
	"BucketNotEmpty":               "The bucket you tried to delete is not empty.",
	"NoSuchBucketPolicy":           "The bucket policy does not exist.",
	"NoSuchLifecycleConfiguration": "The lifecycle configuration does not exist.",
	"NoSuchWebsiteConfiguration":   "The website configuration does not exist.",
	"NoSuchTagSet":                 "The tag set does not exist.",
	"NoSuchKey":                    "The specified key does not exist.",
	"InvalidRange":                 "The requested range is not satisfiable",
	"PreconditionFailed":           "At least one of the preconditions you specified did not hold.",
	"NoSuchUpload":                 "The specified multipart upload does not exist.",
	"InvalidArgument":              "",
	"BadDigest":                    "The Content-MD5 or encryption-key MD5 you specified did not match what was received.",
	"IncompleteBody":               "You did not provide the number of bytes specified by the Content-Length HTTP header.",
	"AccessDenied":                 "Access denied.",
	"AccessDenied.Locked":          "Object is under retention lock (WORM).",
	"ObjectCorrupt":                "Object is marked as corrupt.",
	"QuotaExceeded":                "The tenant storage quota has been exceeded.",
	"ServiceUnavailable":           "The tenant entitlement projection is unavailable.",
	"NotImplemented":               "A requested feature is not implemented.",
}

var errToS3Code = []struct {
	err  error
	code string
}{
	{errMalformedXML, "MalformedXML"},
	{mw.ErrBodyTooLarge, "EntityTooLarge"},
	{errNoSuchBucket, "NoSuchBucket"},
	{errBucketNotEmpty, "BucketNotEmpty"},
	{errNoSuchBucketPolicy, "NoSuchBucketPolicy"},
	{errNoSuchLifecycle, "NoSuchLifecycleConfiguration"},
	{errNoSuchWebsite, "NoSuchWebsiteConfiguration"},
	{errNoSuchTagSet, "NoSuchTagSet"},
	{service.ErrNotFound, "NoSuchKey"},
	{repository.ErrNotFound, "NoSuchKey"},
	{service.ErrInvalidArgs, "InvalidArgument"},
	{service.ErrMetadataTooLarge, "InvalidArgument"},
	{service.ErrMetadataKeyTooLong, "InvalidArgument"},
	{service.ErrMetadataValueTooLong, "InvalidArgument"},
	{service.ErrBadDigest, "BadDigest"},
	{service.ErrSizeMismatch, "IncompleteBody"},
	{service.ErrRangeNotSatisfiable, "InvalidRange"},
	{service.ErrPreconditionFailed, "PreconditionFailed"},
	{service.ErrUploadNotFound, "NoSuchUpload"},
	{repository.ErrUploadNotFound, "NoSuchUpload"},
	{service.ErrLocked, "AccessDenied.Locked"},
	{service.ErrQuotaExceeded, "QuotaExceeded"},
	{service.ErrEntitlementUnavailable, "ServiceUnavailable"},
	{service.ErrForbidden, "AccessDenied"},
	{service.ErrObjectCorrupt, "ObjectCorrupt"},
}

func s3ErrorCode(err error) string {
	for _, m := range errToS3Code {
		if errors.Is(err, m.err) {
			return m.code
		}
	}
	return "InternalError"
}

func s3ErrorResponse(code string) (int, string) {
	status, ok := s3CodeStatus[code]
	if !ok {
		return http.StatusInternalServerError, ""
	}
	return status, s3CodeMessage[code]
}

func classify(err error) (string, string, int) {
	code := s3ErrorCode(err)
	if code == "InvalidArgument" || code == "InternalError" {
		status, _ := s3ErrorResponse(code)
		return code, err.Error(), status
	}
	status, msg := s3ErrorResponse(code)
	return code, msg, status
}
