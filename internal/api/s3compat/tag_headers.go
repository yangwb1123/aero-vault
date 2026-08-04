package s3compat

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/aero-vault/aero-vault/internal/service"
)

func parseTaggingHeader(header http.Header) (map[string]string, error) {
	raw := header.Get("x-amz-tagging")
	if raw == "" {
		return nil, nil
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid x-amz-tagging", service.ErrInvalidArgs)
	}
	if len(values) > 10 {
		return nil, fmt.Errorf("%w: at most 10 object tags are allowed", service.ErrInvalidArgs)
	}
	tags := make(map[string]string, len(values))
	for key, entries := range values {
		if key == "" || len(entries) != 1 {
			return nil, fmt.Errorf("%w: invalid x-amz-tagging", service.ErrInvalidArgs)
		}
		tags[key] = entries[0]
	}
	return tags, nil
}

func tagsFromSet(items []s3Tag) (map[string]string, error) {
	if len(items) > 10 {
		return nil, fmt.Errorf("%w: at most 10 object tags are allowed", service.ErrInvalidArgs)
	}
	tags := make(map[string]string, len(items))
	for _, item := range items {
		if item.Key == "" {
			return nil, fmt.Errorf("%w: object tag key cannot be empty", service.ErrInvalidArgs)
		}
		if _, duplicate := tags[item.Key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate object tag key", service.ErrInvalidArgs)
		}
		tags[item.Key] = item.Value
	}
	return tags, nil
}
