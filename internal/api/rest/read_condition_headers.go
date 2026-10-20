package rest

import (
	"net/http"
	"strings"
)

// Read ETag conditions are lists, not just Header.Get's first field line.
// Preserve presence separately so an empty/invalid INM never enables IMS.
// Filter empty physical lines without changing the existing tag-list parser.
func readETagCondition(r *http.Request, name string) (value string, present bool) {
	fields := r.Header.Values(name)
	values := make([]string, 0, len(fields))
	for _, field := range fields {
		if strings.Trim(field, " \t") != "" {
			values = append(values, field)
		}
	}
	return strings.Join(values, ","), len(fields) > 0
}

// Dates and Range are single-valued at this boundary. Repeated field lines
// are unusable; never silently select a first value. If-Range also needs to
// distinguish an absent field from an invalid/empty one, so checks its count.
func singleReadCondition(r *http.Request, name string) string {
	fields := r.Header.Values(name)
	if len(fields) != 1 {
		return ""
	}
	return fields[0]
}
