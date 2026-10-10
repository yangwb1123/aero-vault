package rest

import "net/http"

func init() {
	view := apiQueryParameter{
		Name: "view", Type: "string",
		Description: "all (default): all readable objects; mine: readable objects owned by the current tenant-bound user or service. Ownership never bypasses ACL. Other values return 400; mine without a valid identity returns 403.",
	}
	RegisterRoutes([]apiRoute{
		{
			Method: "GET", Path: "/v1/files", Summary: "List readable objects", Tag: "files", Status: http.StatusOK,
			Query: []apiQueryParameter{
				{Name: "prefix", Description: "Only keys with this prefix"},
				{Name: "marker", Description: "Resume after this key within the selected view"},
				{Name: "limit", Description: "Maximum objects per page (up to 1000)", Type: "integer"},
				{Name: "deleted", Description: "true selects soft-deleted objects", Type: "boolean"},
				view,
			},
			Response:  `{"objects":[{"key":"file.txt","size":100}],"next_marker":"file.txt","has_more":true}`,
			Responses: map[int]string{400: "Invalid view", 403: "Forbidden or mine view without a valid user/service identity"},
		},
		{
			Method: "GET", Path: "/v1/folders", Summary: "List readable folder contents", Tag: "files", Status: http.StatusOK,
			Query: []apiQueryParameter{
				{Name: "path", Description: "Folder prefix"},
				{Name: "marker", Description: "Resume after this object key within the same path/view. Directory names may repeat across pages; deduplicate by type and name."},
				{Name: "limit", Description: "Visible objects scanned per page, not direct entries; default/maximum 1000", Type: "integer"},
				view,
			},
			Response:  `{"prefix":"","items":[{"name":"docs","type":"folder"}],"has_more":true,"next_marker":"docs/report.txt"}`,
			Responses: map[int]string{400: "Invalid view", 403: "Forbidden or mine view without a valid user/service identity"},
		},
	})
}
