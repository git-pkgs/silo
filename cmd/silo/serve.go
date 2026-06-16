package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/git-pkgs/silo/internal/config"
	"github.com/git-pkgs/silo/internal/gitstore"
	"github.com/git-pkgs/silo/internal/hooks"
	githttp "github.com/git-pkgs/silo/internal/http/git"
	"github.com/git-pkgs/silo/internal/http/web"
	"github.com/git-pkgs/silo/internal/receive"
	"github.com/git-pkgs/silo/internal/signer"
	siloSSH "github.com/git-pkgs/silo/internal/ssh"
	"github.com/git-pkgs/silo/internal/store"
)

func newServeCmd(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the HTTP and SSH listeners",
		Long: `Start silo's listeners. HTTP serves anonymous git-upload-pack and the
web UI; SSH serves authenticated upload-pack and receive-pack.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serve(cmd.Context(), *cfg)
		},
	}
	cmd.Flags().StringVar(&cfg.HTTPAddr, "http", cfg.HTTPAddr, "HTTP listen address ($SILO_HTTP)")
	cmd.Flags().StringVar(&cfg.SSHAddr, "ssh", cfg.SSHAddr, "SSH listen address ($SILO_SSH)")
	cmd.Flags().StringVar(&cfg.BaseURL, "base-url", cfg.BaseURL, "public base URL ($SILO_BASE_URL)")
	return cmd
}

const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 5 * time.Second
)

func isGitTransportPath(p string) bool {
	return strings.HasSuffix(p, "/info/refs") ||
		strings.HasSuffix(p, "/git-upload-pack") ||
		strings.HasSuffix(p, "/git-receive-pack")
}

func serve(ctx context.Context, cfg config.Config) error {
	if os.Getenv("SILO_DEBUG") != "" {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	gst, err := gitstore.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	hostKey, err := loadOrCreateHostKey(cfg.DataDir)
	if err != nil {
		return err
	}
	sgn, err := signer.Load(cfg.DataDir)
	if err != nil {
		return err
	}
	h := func() receive.Hooks { return &hooks.Builtin{BaseURL: cfg.BaseURL, Signer: sgn} }

	gitH := githttp.Handler(gst)
	webH := web.Handler(st, gst, cfg.BaseURL, sgn.ID())
	staticH := http.StripPrefix("/static/", web.StaticHandler())
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/static/"):
			staticH.ServeHTTP(w, r)
			return
		case isGitTransportPath(r.URL.Path):
			gitH.ServeHTTP(w, r)
			return
		}
		webH.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: readHeaderTimeout}
	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	slog.Info("http listening", "addr", ln.Addr().String(), "data", cfg.DataDir)

	const numServers = 2
	errc := make(chan error, numServers)
	go func() { errc <- srv.Serve(ln) }()
	go func() {
		errc <- siloSSH.Serve(ctx, cfg.SSHAddr, hostKey, st, gst, h, receive.DefaultLimits())
	}()

	select {
	case <-ctx.Done():
		shctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(shctx)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
