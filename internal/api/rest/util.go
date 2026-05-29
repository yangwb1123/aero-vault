package rest

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// chiURLParam is a tiny indirection so handlers in this package don't all
// import chi directly when they only need URL params.
func chiURLParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}
