// Package store is the silo.db sqlite layer: users, SSH keys, tokens, repos,
// and the background job queue.
package store

import (
	"database/sql"
	_ "embed"
	"errors"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// Store wraps the silo.db connection.
type Store struct {
	db *sql.DB
}

// Open opens (creating if absent) silo.db under dataDir and applies the
// schema.
func Open(dataDir string) (*Store, error) {
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "silo.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// DB returns the underlying handle for callers that need ad-hoc queries.
func (s *Store) DB() *sql.DB { return s.db }

// User is a registered account.
type User struct {
	ID        int64
	Name      string
	CreatedAt time.Time
}

// SSHKey is a public key registered to a user.
type SSHKey struct {
	ID          int64
	UserID      int64
	Fingerprint string
	PubKey      string
	CreatedAt   time.Time
}

// Repo is a hosted repository.
type Repo struct {
	ID        int64
	Owner     string
	Name      string
	CreatedAt time.Time
}

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("store: not found")

// CreateUser inserts a user and returns it.
func (s *Store) CreateUser(name string) (*User, error) {
	now := time.Now().UTC()
	res, err := s.db.Exec(`INSERT INTO users (name, created_at) VALUES (?, ?)`, name, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &User{ID: id, Name: name, CreatedAt: now}, nil
}

// AddSSHKey registers a public key for a user.
func (s *Store) AddSSHKey(userID int64, fingerprint, pubkey string) (*SSHKey, error) {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO ssh_keys (user_id, fingerprint, pubkey, created_at) VALUES (?, ?, ?, ?)`,
		userID, fingerprint, pubkey, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &SSHKey{ID: id, UserID: userID, Fingerprint: fingerprint, PubKey: pubkey, CreatedAt: now}, nil
}

// UserBySSHFingerprint returns the user owning the key with the given
// SHA256 fingerprint.
func (s *Store) UserBySSHFingerprint(fp string) (*User, error) {
	var u User
	err := s.db.QueryRow(
		`SELECT u.id, u.name, u.created_at FROM users u
		 JOIN ssh_keys k ON k.user_id = u.id WHERE k.fingerprint = ?`, fp).
		Scan(&u.ID, &u.Name, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

// CreateRepo inserts a repo record.
func (s *Store) CreateRepo(owner, name string) (*Repo, error) {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO repos (owner, name, created_at) VALUES (?, ?, ?)`, owner, name, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &Repo{ID: id, Owner: owner, Name: name, CreatedAt: now}, nil
}

// ListRepos returns all repos ordered by owner, name.
func (s *Store) ListRepos() ([]Repo, error) {
	rows, err := s.db.Query(`SELECT id, owner, name, created_at FROM repos ORDER BY owner, name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Repo
	for rows.Next() {
		var r Repo
		if err := rows.Scan(&r.ID, &r.Owner, &r.Name, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RepoByPath looks up a repo by owner/name.
func (s *Store) RepoByPath(owner, name string) (*Repo, error) {
	var r Repo
	err := s.db.QueryRow(
		`SELECT id, owner, name, created_at FROM repos WHERE owner = ? AND name = ?`,
		owner, name).Scan(&r.ID, &r.Owner, &r.Name, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}
