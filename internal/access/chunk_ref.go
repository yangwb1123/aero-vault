package access

import (
	"context"
	"fmt"
)

// ChunkRef identifies one object's chunks, including the exact loaded version's
// ObjectID. It is an internal identity, not an authorization grant.
type ChunkRef struct {
	TenantID string
	Bucket   string
	Key      string
	ObjectID int64
}

// ValidateChunkRef requires explicit scope and a positive object ID. It neither
// normalizes values nor applies key-naming or authorization policy.
func ValidateChunkRef(ref ChunkRef) error {
	if ref.TenantID == "" || ref.Bucket == "" || ref.Key == "" {
		return fmt.Errorf("%w: incomplete chunk scope", ErrInvalidArgument)
	}
	if ref.ObjectID <= 0 {
		return fmt.Errorf("%w: chunk object ID must be positive", ErrInvalidArgument)
	}
	return nil
}

// ScopedChunkCleaner cleans only the chunks identified by the complete ref.
// Implementations must not interpret a valid ref as an authorization grant.
type ScopedChunkCleaner interface {
	DeleteScopedObjectChunks(ctx context.Context, ref ChunkRef) error
}
