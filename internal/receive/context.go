package receive

import "context"

// Pusher identifies the authenticated user behind a push, as resolved by the
// transport (SSH public-key auth). It is threaded through ctx so hooks can
// name it in rejection messages and RSL annotations.
type Pusher struct {
	User           string
	KeyFingerprint string
}

type pusherKey struct{}

// WithPusher returns a context carrying p.
func WithPusher(ctx context.Context, p Pusher) context.Context {
	return context.WithValue(ctx, pusherKey{}, p)
}

// PusherFrom returns the Pusher stored in ctx, if any.
func PusherFrom(ctx context.Context) (Pusher, bool) {
	p, ok := ctx.Value(pusherKey{}).(Pusher)
	return p, ok
}

type repoPathKey struct{}

// WithRepoPath returns a context carrying the bare-repo filesystem path, for
// hooks that need to open the repo via something other than go-git (gittuf).
func WithRepoPath(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, repoPathKey{}, path)
}

// RepoPathFrom returns the bare-repo path stored in ctx, if any.
func RepoPathFrom(ctx context.Context) (string, bool) {
	p, ok := ctx.Value(repoPathKey{}).(string)
	return p, ok
}
