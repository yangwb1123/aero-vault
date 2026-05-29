package repository

import (
	"context"
	"fmt"
	"time"
)

// AcquireLease atomically grants or renews the named singleton lease to holder
// for ttl, returning true iff holder now holds it. A holder may always renew
// its own lease; a different holder may take over only once the existing lease
// has expired (now > expires_at). The two-step UPDATE-then-INSERT approach is
// dialect-portable across SQLite (modernc) and Postgres.
func (s *sqlStore) AcquireLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error) {
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)
	expiryStr := now.Add(ttl).Format(time.RFC3339Nano)

	// 1. Claim a free/expired lease, or renew our own. $1 and $4 are both the
	// holder but use distinct placeholders so rebind's positional $N→? rewrite
	// binds each from its own arg; likewise the value is passed twice.
	res, err := s.db.ExecContext(ctx, s.rebind(
		`UPDATE leases SET holder=$1, expires_at=$2 WHERE name=$3 AND (holder=$4 OR expires_at < $5)`),
		holder, expiryStr, name, holder, nowStr)
	if err != nil {
		return false, fmt.Errorf("acquire lease %q: update: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, nil
	}

	// 2. No row updated: the lease row may not exist yet. Try to create it;
	// ON CONFLICT DO NOTHING means a concurrently-held lease leaves us empty-handed.
	res, err = s.db.ExecContext(ctx, s.rebind(
		`INSERT INTO leases (name, holder, expires_at) VALUES ($1,$2,$3) ON CONFLICT (name) DO NOTHING`),
		name, holder, expiryStr)
	if err != nil {
		return false, fmt.Errorf("acquire lease %q: insert: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, nil
	}

	// 3. A valid lease is held by someone else.
	return false, nil
}
