// Package gitstore opens and creates bare repositories under a data directory
// using go-git's filesystem storage.
package gitstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// Store locates bare repositories under DataDir/repos/<owner>/<name>.git.
type Store struct {
	root string
}

const dirPerm = 0o750

// Open returns a Store rooted at dataDir. The repos/ subdirectory is created
// if absent.
func Open(dataDir string) (*Store, error) {
	root := filepath.Join(dataDir, "repos")
	if err := os.MkdirAll(root, dirPerm); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ErrInvalidName is returned when an owner or repo name contains characters
// outside the allowed set.
var ErrInvalidName = errors.New("gitstore: invalid owner or repo name")

// Path returns the absolute filesystem path for owner/name, or ErrInvalidName
// if either component would escape the store root.
func (s *Store) Path(owner, name string) (string, error) {
	if !nameRE.MatchString(owner) || !nameRE.MatchString(name) {
		return "", ErrInvalidName
	}
	return filepath.Join(s.root, owner, name+".git"), nil
}

// Repo opens the bare repository at owner/name. It returns
// transport.ErrRepositoryNotFound if the directory does not exist.
func (s *Store) Repo(owner, name string) (*git.Repository, error) {
	p, err := s.Path(owner, name)
	if err != nil {
		return nil, err
	}
	repo, err := git.PlainOpenWithOptions(p, &git.PlainOpenOptions{DetectDotGit: false})
	if errors.Is(err, git.ErrRepositoryNotExists) {
		return nil, transport.ErrRepositoryNotFound
	}
	return repo, err
}

// Init creates a new bare repository at owner/name with HEAD pointing at
// refs/heads/main.
func (s *Store) Init(owner, name string) (*git.Repository, error) {
	p, err := s.Path(owner, name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(p); err == nil {
		return nil, fmt.Errorf("gitstore: %s/%s already exists", owner, name)
	}
	if err := os.MkdirAll(filepath.Dir(p), dirPerm); err != nil {
		return nil, err
	}
	return git.PlainInitWithOptions(p, &git.PlainInitOptions{
		Bare:        true,
		InitOptions: git.InitOptions{DefaultBranch: plumbing.ReferenceName("refs/heads/main")},
	})
}
