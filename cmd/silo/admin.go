package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	xssh "golang.org/x/crypto/ssh"

	"github.com/git-pkgs/silo/internal/config"
	"github.com/git-pkgs/silo/internal/gitstore"
	"github.com/git-pkgs/silo/internal/store"
)

func newAdminCmd(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Administrative commands run against the data directory",
	}
	cmd.AddCommand(newAdminUserCmd(cfg), newAdminRepoCmd(cfg))
	return cmd
}

func newAdminUserCmd(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{Use: "user", Short: "Manage users"}
	var keyPath string
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a user and register an SSH public key",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			st, err := store.Open(cfg.DataDir)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			u, err := st.CreateUser(args[0])
			if err != nil {
				return err
			}
			if keyPath != "" {
				if err := addKey(st, u.ID, keyPath); err != nil {
					return err
				}
			}
			fmt.Println("created user", u.Name)
			return nil
		},
	}
	create.Flags().StringVar(&keyPath, "ssh-key", "", "path to an SSH public key to register")
	cmd.AddCommand(create)
	return cmd
}

func addKey(st *store.Store, userID int64, path string) error {
	// #nosec G304 -- path is supplied by an administrator on the local CLI
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	pk, _, _, _, err := xssh.ParseAuthorizedKey(b)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	_, err = st.AddSSHKey(userID, xssh.FingerprintSHA256(pk), string(b))
	return err
}

func newAdminRepoCmd(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{Use: "repo", Short: "Manage repositories"}
	create := &cobra.Command{
		Use:   "create <owner>/<name>",
		Short: "Create a bare repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			owner, name, ok := strings.Cut(args[0], "/")
			if !ok {
				return errors.New("expected <owner>/<name>")
			}
			st, err := store.Open(cfg.DataDir)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			gst, err := gitstore.Open(cfg.DataDir)
			if err != nil {
				return err
			}
			if _, err := gst.Init(owner, name); err != nil {
				return err
			}
			if _, err := st.CreateRepo(owner, name); err != nil {
				return err
			}
			fmt.Println("created repo", args[0])
			return nil
		},
	}
	cmd.AddCommand(create)
	return cmd
}
