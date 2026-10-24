package rest

import (
	"context"
	"errors"
	"net/http"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

// classifyBoundary precedes legacy sentinel and raw-text mappings. A provider
// fault may wrap NotFound, Denied, or protocol errors without becoming absence
// or a permission decision. Only fixed messages cross the protocol boundary.
func classifyBoundary(err error) (code, message string, status int, ok bool) {
	fault := access.IsResourceBoundaryFault(err)
	denied := access.IsResourceBoundaryDenied(err)
	if !fault && !denied && !errors.Is(err, access.ErrSpaceBound) {
		return "", "", 0, false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, service.ErrTimeout) {
		return "Timeout", "request timed out", http.StatusGatewayTimeout, true
	}
	if fault {
		return "InternalError", "resource boundary unavailable", http.StatusInternalServerError, true
	}
	if errors.Is(err, spaces.ErrTenantUnavailable) {
		return "TenantDisabled", "tenant is unavailable", http.StatusForbidden, true
	}
	return "AccessDenied", "access denied", http.StatusForbidden, true
}
