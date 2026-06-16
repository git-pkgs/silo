package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/git-pkgs/silo/internal/config"
	"github.com/git-pkgs/silo/internal/gitstore"
	githttp "github.com/git-pkgs/silo/internal/http/git"
	"github.com/git-pkgs/silo/internal/receive"
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

func serve(ctx context.Context, cfg config.Config) error {
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

	mux := http.NewServeMux()
	mux.Handle("/", githttp.Handler(gst))
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
		errc <- siloSSH.Serve(ctx, cfg.SSHAddr, hostKey, st, gst, receive.NoopHooks{}, receive.DefaultLimits())
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
