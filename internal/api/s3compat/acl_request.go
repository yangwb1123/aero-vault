package s3compat

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
)

func cannedACLFromRequest(r *http.Request) (string, error) {
	if acl := r.Header.Get("x-amz-acl"); acl != "" {
		return acl, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, bucketConfigBodyLimit))
	if err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return "private", nil
	}
	var policy accessControlPolicy
	if err := xml.Unmarshal(body, &policy); err != nil {
		return "", errMalformedXML
	}
	return policyToCanned(policy), nil
}
