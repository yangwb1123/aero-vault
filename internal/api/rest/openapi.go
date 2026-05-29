package rest

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.json
var openapiJSON []byte

// OpenAPISpecHandler serves /openapi.json.
func OpenAPISpecHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(openapiJSON)
	}
}

// SwaggerUIHandler serves a minimal Swagger UI page at /docs that loads
// /openapi.json from the same origin. No JS framework dependency — just the
// CDN-hosted swagger-ui assets.
func SwaggerUIHandler() http.HandlerFunc {
	html := `<!doctype html>
<html><head><meta charset="utf-8"><title>aero-vault API</title>
<link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
<style>body{margin:0;background:#fafafa}</style></head>
<body><div id="ui"></div>
<script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
<script>
window.onload = () => SwaggerUIBundle({ url: '/openapi.json', dom_id: '#ui', deepLinking: true });
</script></body></html>`
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}
}
