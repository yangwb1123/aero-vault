package rest

import "encoding/json"

func routeSuccessResponse(r apiRoute) map[string]any {
	response := map[string]any{"description": "Success"}
	if r.BinaryResponse {
		response["content"] = binaryReadContent()
	} else if r.Response != "" {
		response["content"] = map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{"type": "object"}, "example": json.RawMessage(r.Response),
			},
		}
	}
	return response
}

func binaryReadContent() map[string]any {
	// Raw objects retain their own Content-Type; thumbnail representation is
	// JPEG. A media wildcard documents the stream without inventing a JSON DTO.
	return map[string]any{"*/*": map[string]any{
		"schema": map[string]any{"type": "string", "format": "binary"},
	}}
}

func routeErrorResponse() map[string]any {
	return map[string]any{
		"description": "Error response",
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"error": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"code": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"},
							},
						},
					},
				},
			},
		},
	}
}
