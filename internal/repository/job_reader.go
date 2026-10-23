package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrJobReaderUnavailable reports that a repository does not expose the raw
// SQL store needed for exact job reads. Billing/audit wrappers must not be
// treated as the authoritative reader.
var ErrJobReaderUnavailable = errors.New("repository: exact job reader unavailable")

// JobLookup is the result of an exact job-id read. Exactly one of Present and
// Missing is true on success; RequestedID always echoes the caller's id so a
// consumer can reject a row that does not match the requested identity.
type JobLookup struct {
	RequestedID int64
	Job         Job
	Present     bool
	Missing     bool
}

// JobReader performs an exact lookup by job id without modifying job state.
type JobReader interface {
	ReadJob(ctx context.Context, id int64) (JobLookup, error)
}

type sqlJobReader struct {
	store *sqlStore
}

// NewJobReader captures the raw SQL store before any billing/audit wrapper is
// applied. It does not add a method to Repository and does not mutate jobs.
func NewJobReader(repo Repository) (JobReader, error) {
	store, ok := repo.(*sqlStore)
	if !ok || store == nil || store.db == nil {
		return nil, ErrJobReaderUnavailable
	}
	return &sqlJobReader{store: store}, nil
}

// ReadJob loads the job with exactly the requested id. A genuine absence is a
// successful Missing result; scan, persistence and context failures remain
// errors so callers cannot mistake a fault for an absent job.
func (r *sqlJobReader) ReadJob(ctx context.Context, id int64) (JobLookup, error) {
	if id <= 0 {
		return JobLookup{}, fmt.Errorf("read job: id must be positive")
	}
	row := r.store.db.QueryRowContext(ctx, r.store.rebind(
		`SELECT `+jobCols+` FROM jobs WHERE id=$1`), id)
	job, err := scanJobRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return JobLookup{RequestedID: id, Missing: true}, nil
	}
	if err != nil {
		return JobLookup{}, fmt.Errorf("read job %d: %w", id, err)
	}
	if job.ID != id || job.ID <= 0 {
		return JobLookup{}, fmt.Errorf("read job %d: scanned job id %d", id, job.ID)
	}
	return JobLookup{RequestedID: id, Job: job, Present: true}, nil
}
