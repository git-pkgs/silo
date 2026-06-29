package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Job is one row of the background-job queue. Kind is the dispatcher key
// (e.g. "pkgs-reindex"); Payload is opaque JSON understood by the handler.
type Job struct {
	ID        int64
	RepoID    int64
	Kind      string
	State     string // pending | running | done | failed
	Payload   string
	Attempts  int
	UpdatedAt time.Time
}

const (
	JobPending = "pending"
	JobRunning = "running"
	JobDone    = "done"
	JobFailed  = "failed"

	// MaxJobAttempts gates re-claim. A row that has reached this many
	// attempts and is not pending stays off the work queue.
	MaxJobAttempts = 3
)

// EnqueueJob inserts a pending row for (repoID, kind, payload) unless an
// identical pending row already exists. Returns the inserted (or existing)
// row's id.
func (s *Store) EnqueueJob(repoID int64, kind, payload string) (int64, error) {
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var existing int64
	err = tx.QueryRow(
		`SELECT id FROM jobs WHERE repo_id = ? AND kind = ? AND payload = ? AND state = ? LIMIT 1`,
		repoID, kind, payload, JobPending,
	).Scan(&existing)
	if err == nil {
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	res, err := tx.Exec(
		`INSERT INTO jobs (repo_id, kind, state, payload, attempts, updated_at)
		 VALUES (?, ?, ?, ?, 0, ?)`,
		repoID, kind, JobPending, payload, now,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// ClaimJob atomically picks the oldest pending row whose kind is in the given
// set, marks it running, bumps attempts, and returns it. Returns (nil, nil)
// when nothing is claimable. Skips rows whose attempts have already exceeded
// MaxJobAttempts.
func (s *Store) ClaimJob(kinds ...string) (*Job, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	now := time.Now().UTC()

	placeholders := strings.TrimRight(strings.Repeat("?,", len(kinds)), ",")

	query := fmt.Sprintf(`
		UPDATE jobs SET state = 'running', attempts = attempts + 1, updated_at = ?
		WHERE id = (
			SELECT id FROM jobs
			WHERE state = 'pending' AND attempts < ? AND kind IN (%s)
			ORDER BY id LIMIT 1
		)
		RETURNING id, repo_id, kind, state, payload, attempts, updated_at`,
		placeholders)

	finalArgs := make([]any, 0, len(kinds)+2) //nolint:mnd // now + MaxJobAttempts + kinds
	finalArgs = append(finalArgs, now, MaxJobAttempts)
	for _, k := range kinds {
		finalArgs = append(finalArgs, k)
	}

	var j Job
	var repoID sql.NullInt64
	err := s.db.QueryRow(query, finalArgs...).Scan(
		&j.ID, &repoID, &j.Kind, &j.State, &j.Payload, &j.Attempts, &j.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if repoID.Valid {
		j.RepoID = repoID.Int64
	}
	return &j, nil
}

// CompleteJob marks a job done or failed and stamps updated_at.
func (s *Store) CompleteJob(id int64, state string) error {
	if state != JobDone && state != JobFailed {
		return fmt.Errorf("store: invalid completion state %q", state)
	}
	_, err := s.db.Exec(
		`UPDATE jobs SET state = ?, updated_at = ? WHERE id = ?`,
		state, time.Now().UTC(), id,
	)
	return err
}

// JobsForRepo returns all jobs of the given kind for the repo, newest first.
// Pass kind = "" to return every kind.
func (s *Store) JobsForRepo(repoID int64, kind string) ([]Job, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if kind == "" {
		rows, err = s.db.Query(
			`SELECT id, COALESCE(repo_id, 0), kind, state, payload, attempts, updated_at
			 FROM jobs WHERE repo_id = ? ORDER BY id DESC`, repoID)
	} else {
		rows, err = s.db.Query(
			`SELECT id, COALESCE(repo_id, 0), kind, state, payload, attempts, updated_at
			 FROM jobs WHERE repo_id = ? AND kind = ? ORDER BY id DESC`, repoID, kind)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.RepoID, &j.Kind, &j.State, &j.Payload, &j.Attempts, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// HasPendingOrRunning reports whether the repo has any job of the given kind
// in state pending or running. Used by the API to flag "indexing…" status.
func (s *Store) HasPendingOrRunning(repoID int64, kind string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM jobs WHERE repo_id = ? AND kind = ? AND state IN ('pending','running')`,
		repoID, kind,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
