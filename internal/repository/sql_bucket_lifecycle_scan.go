package repository

import "context"

const lifecycleScanBatchSize = 200

type lifecycleRowScanner func(lifecycleRows) (Object, bool, error)

type lifecycleRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

func lifecycleScanLimits(limit int) (int, int) {
	if limit <= 0 {
		limit = lifecycleScanBatchSize
	}
	batch := limit
	if batch > lifecycleScanBatchSize {
		batch = lifecycleScanBatchSize
	}
	return limit, batch
}

// scanLifecycleBatches walks candidates by object id until the requested
// number of eligible rows is collected. Lifecycle predicates are partly
// represented as JSON and timestamps, so filtering after a fixed SQL LIMIT
// can otherwise hide eligible rows behind fresh or malformed candidates.
func (s *sqlStore) scanLifecycleBatches(
	ctx context.Context,
	limit, batch int,
	query string,
	fixedArgs []any,
	scan lifecycleRowScanner,
) ([]Object, error) {
	out := make([]Object, 0, min(limit, batch))
	var cursor int64
	for len(out) < limit {
		args := make([]any, 0, len(fixedArgs)+2)
		args = append(args, cursor)
		args = append(args, fixedArgs...)
		args = append(args, batch)
		rows, err := s.db.QueryContext(ctx, s.rebind(query), args...)
		if err != nil {
			return nil, err
		}
		scanned := 0
		for rows.Next() {
			obj, eligible, err := scan(rows)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			scanned++
			cursor = obj.ID
			if eligible {
				out = append(out, obj)
				if len(out) >= limit {
					break
				}
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if scanned < batch {
			break
		}
	}
	return out, nil
}
