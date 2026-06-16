// Package ssh serves git-upload-pack and git-receive-pack over SSH.
// Public-key auth resolves the user via store.UserBySSHFingerprint and
// threads it into the receive context for hooks to name in RSL annotations.
package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	gssh "github.com/gliderlabs/ssh"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	xssh "golang.org/x/crypto/ssh"

	"github.com/git-pkgs/silo/internal/gitstore"
	"github.com/git-pkgs/silo/internal/receive"
	"github.com/git-pkgs/silo/internal/store"
)

// HooksFactory returns a fresh Hooks instance per push, since some hook
// implementations hold per-push state (the flock handle).
type HooksFactory func() receive.Hooks

// Serve listens on addr and handles git-over-SSH sessions until ctx is
// cancelled. hostKey is the server's private host key.
func Serve(ctx context.Context, addr string, hostKey gssh.Signer, st *store.Store, gst *gitstore.Store, hf HooksFactory, limits receive.Limits) error {
	srv := &gssh.Server{
		Addr:             addr,
		Handler:          handler(gst, hf, limits),
		PublicKeyHandler: publicKeyHandler(st),
	}
	srv.AddHostKey(hostKey)

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("ssh listening", "addr", addr)

	select {
	case <-ctx.Done():
		return srv.Close()
	case err := <-errc:
		if errors.Is(err, gssh.ErrServerClosed) {
			return nil
		}
		return err
	}
}

const (
	ctxUserKey     = "silo.user"
	cmdUploadPack  = "git-upload-pack"
	cmdReceivePack = "git-receive-pack"
)

func publicKeyHandler(st *store.Store) gssh.PublicKeyHandler {
	return func(ctx gssh.Context, key gssh.PublicKey) bool {
		fp := xssh.FingerprintSHA256(key)
		u, err := st.UserBySSHFingerprint(fp)
		if err != nil {
			return false
		}
		ctx.SetValue(ctxUserKey, receive.Pusher{User: u.Name, KeyFingerprint: fp})
		return true
	}
}

func handler(gst *gitstore.Store, hf HooksFactory, limits receive.Limits) gssh.Handler {
	upSrv := server.NewServer(&loader{gst: gst})

	return func(s gssh.Session) {
		cmd, owner, name, err := parseExec(s.RawCommand())
		if err != nil {
			fail(s, err.Error())
			return
		}
		repo, err := gst.Repo(owner, name)
		if err != nil {
			fail(s, "repository not found")
			return
		}
		repoPath, _ := gst.Path(owner, name)

		switch cmd {
		case cmdUploadPack:
			ep, _ := transport.NewEndpoint("/" + owner + "/" + name + ".git")
			sess, err := upSrv.NewUploadPackSession(ep, nil)
			if err != nil {
				fail(s, err.Error())
				return
			}
			defer func() { _ = sess.Close() }()
			if err := serveUploadPack(s.Context(), sess, s); err != nil {
				slog.Warn("upload-pack", "repo", owner+"/"+name, "err", err)
			}
		case cmdReceivePack:
			var rctx context.Context = s.Context()
			if p, ok := rctx.Value(ctxUserKey).(receive.Pusher); ok {
				rctx = receive.WithPusher(rctx, p)
			}
			rctx = receive.WithRepoPath(rctx, repoPath)
			if err := receive.Advertise(repo, s); err != nil {
				slog.Warn("advertise", "err", err)
				return
			}
			if err := receive.ReceivePack(rctx, repo, s, s, hf(), limits); err != nil {
				slog.Warn("receive-pack", "repo", owner+"/"+name, "err", err)
			}
		}
		_ = s.Exit(0)
	}
}

func serveUploadPack(ctx context.Context, sess transport.UploadPackSession, rw io.ReadWriter) error {
	ar, err := sess.AdvertisedReferencesContext(ctx)
	if err != nil {
		return err
	}
	if err := ar.Encode(rw); err != nil {
		return err
	}
	req := packp.NewUploadPackRequest()
	if err := req.Decode(rw); err != nil {
		return err
	}
	resp, err := sess.UploadPack(ctx, req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Close() }()
	return resp.Encode(rw)
}

type loader struct{ gst *gitstore.Store }

func (l *loader) Load(ep *transport.Endpoint) (storer.Storer, error) { //nolint:ireturn // implements server.Loader
	p := strings.TrimSuffix(strings.TrimPrefix(ep.Path, "/"), ".git")
	seg := strings.SplitN(p, "/", expectedExecParts)
	if len(seg) != expectedExecParts {
		return nil, transport.ErrRepositoryNotFound
	}
	repo, err := l.gst.Repo(seg[0], seg[1])
	if err != nil {
		return nil, err
	}
	return repo.Storer, nil
}

var errBadCommand = errors.New("only git-upload-pack and git-receive-pack are accepted")

const expectedExecParts = 2

// parseExec extracts the git command and owner/name from an SSH exec line of
// the form `git-upload-pack 'owner/repo.git'`. The path is confined to a
// single owner/name pair; anything else is rejected.
func parseExec(raw string) (cmd, owner, name string, err error) {
	parts := strings.SplitN(raw, " ", expectedExecParts)
	if len(parts) != expectedExecParts {
		return "", "", "", errBadCommand
	}
	cmd = parts[0]
	if cmd != cmdUploadPack && cmd != cmdReceivePack {
		return "", "", "", errBadCommand
	}
	path := strings.Trim(parts[1], `'"`)
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	seg := strings.Split(path, "/")
	if len(seg) != expectedExecParts || seg[0] == "" || seg[1] == "" {
		return "", "", "", fmt.Errorf("%w: bad repository path %q", errBadCommand, parts[1])
	}
	return cmd, seg[0], seg[1], nil
}

func fail(s gssh.Session, msg string) {
	_, _ = fmt.Fprintln(s.Stderr(), "silo: "+msg)
	_ = s.Exit(1)
}
