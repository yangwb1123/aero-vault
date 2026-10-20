package rest

import "net/http"

func fileReadRoute() apiRoute {
	return apiRoute{
		Method: "GET", Path: "/v1/files/{key}",
		Summary: "Read current/exact historical content with stored response metadata, or an explicit thumbnail. Reported raw read/write/Close errors abort without an appended error document; delivered bytes cannot be retracted. Thumbnail 200 after conditional re-Stat selects cache/source from the newer authorized observation; opened source encryption metadata can veto resident thumbnail cache storage",
		Tag:     "files", Status: http.StatusOK, BinaryResponse: true,
		Query: fileReadParameters(),
		Responses: map[int]string{
			http.StatusPartialContent:               "Range sliced from the authorized opened current object or exact version; its returned metadata defines Content-Range. Coordinates are unsigned ASCII decimals; oversized ends/suffixes clamp to the selected size. If-Range must match that source's strong ETag or exact Last-Modified, otherwise full content is returned. Repeated Range or If-Range disables partial content; the same selected full content is returned. Raw content keeps the selected attachment, encoding and object metadata. Reported non-cancellation streaming failures (including source Close failures after body copy) abort, never append an error body",
			http.StatusNotModified:                  "The authorized content/thumbnail matches the read validator (If-None-Match field lines form one ETag list; repeated dates are ignored; for thumbnails, empty opaque tags are not wildcards); raw content retains selected attachment, encoding, object digest, storage class and user metadata",
			http.StatusPreconditionFailed:           "The selected object/version fails a read precondition",
			http.StatusRequestedRangeNotSatisfiable: "Range lies outside the selected content (including oversized decimal starts or valid ranges on empty objects); Content-Range carries bytes */size",
			http.StatusBadRequest:                   "Invalid read query/representation or thumbnail arguments; an opened source Content-Type that declares a non-image is rejected before reading; a non-image Content-Type on the second authorized conditional Stat returns 400, not 304",
			http.StatusNotFound:                     "The selected full key or non-empty version ID does not exist; explicit representations never fall back",
			http.StatusUnsupportedMediaType:         "Thumbnail source format or Content-Encoding is unsupported; non-empty encoding on the second authorized conditional Stat returns 415, not 304; an authorized opened source is also checked before sniff/decode. The opened source Content-Type determines declared-format admission or generic-MIME magic sniffing. The second authorized conditional Stat also checks Content-Type declarations (unsupported image formats return 415, not 304)",
			http.StatusRequestEntityTooLarge:        "Thumbnail source exceeds decode/metadata/compressed-source limits",
			http.StatusGone:                         "The selected source is marked corrupt",
			http.StatusGatewayTimeout:               "Thumbnail request deadline expired, including after a completed shared generation; no success output or follower access event. Client cancellation remains silent",
		},
	}
}

func fileHeadRoute() apiRoute {
	return apiRoute{
		Method: "HEAD", Path: "/v1/files/{key}", Summary: "Head the selected current content, exact version or thumbnail; thumbnail 200 after conditional re-Stat selects cache/source from the newer authorized observation; opened source encryption metadata can veto resident thumbnail cache storage", Tag: "files", Status: http.StatusOK,
		Query: fileReadParameters(),
		Responses: map[int]string{
			http.StatusNotModified:           "The authorized selected representation matches the read validator (If-None-Match field lines form one ETag list; repeated dates are ignored; for thumbnails, empty opaque tags are not wildcards); raw content retains selected attachment, encoding, object digest, storage class and user metadata",
			http.StatusPreconditionFailed:    "The selected object/version fails a read precondition",
			http.StatusBadRequest:            "Invalid read query/representation or thumbnail arguments; an opened source Content-Type that declares a non-image is rejected before reading; a non-image Content-Type on the second authorized conditional Stat returns 400, not 304",
			http.StatusNotFound:              "The selected full key or exact version does not exist; no fallback",
			http.StatusUnsupportedMediaType:  "Thumbnail source format or Content-Encoding is unsupported; non-empty encoding on the second authorized conditional Stat returns 415, not 304; an authorized opened source is also checked before sniff/decode. The opened source Content-Type determines declared-format admission or generic-MIME magic sniffing. The second authorized conditional Stat also checks Content-Type declarations (unsupported image formats return 415, not 304)",
			http.StatusRequestEntityTooLarge: "Thumbnail source exceeds decode/metadata/compressed-source limits",
			http.StatusGone:                  "The selected source is marked corrupt",
			http.StatusGatewayTimeout:        "Thumbnail request deadline expired, including after a completed shared generation; no success output or follower access event. Client cancellation remains silent",
		},
	}
}

func fileReadParameters() []apiQueryParameter {
	return []apiQueryParameter{
		{Name: "representation", Description: "content reads the complete opaque Key without subresource dispatch; thumbnail derives JPEG from that exact Key. Must be one non-empty content|thumbnail value. Omitted preserves legacy suffix dispatch."},
		{Name: "version", Description: "Exact historical Version ID. A missing non-empty ID returns 404, never the current object; an empty value retains current-object semantics."},
		{Name: "w", Type: "integer", Description: "Thumbnail width; applies only to thumbnail derivation, with the existing dimension validation/clamping rules."},
		{Name: "h", Type: "integer", Description: "Thumbnail height; applies only to thumbnail derivation, with the existing dimension validation/clamping rules."},
	}
}
