package spaces

import "errors"

var (
	ErrInvalidArgument   = errors.New("spaces: invalid argument")
	ErrNotFound          = errors.New("spaces: not found")
	ErrTenantUnavailable = errors.New("spaces: tenant unavailable")
	ErrConflict          = errors.New("spaces: conflict")
	ErrMemberExists      = errors.New("spaces: member already active")
	ErrMemberRevoked     = errors.New("spaces: member revoked")
	ErrLastAdmin         = errors.New("spaces: last active user admin")
	ErrConcurrent        = errors.New("spaces: concurrent transaction; retry explicitly")
	ErrPersistence       = errors.New("spaces: persistence failure")
)
