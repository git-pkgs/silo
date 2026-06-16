package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/git-pkgs/silo/internal/config"
)

var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	cfg := config.FromEnv()
	root := &cobra.Command{
		Use:          "silo",
		Short:        "A git forge with gittuf built into the receive path",
		Version:      version,
		SilenceUsage: true,
	}
	root.PersistentFlags().StringVar(&cfg.DataDir, "data", cfg.DataDir, "data directory ($SILO_DATA)")

	root.AddCommand(newServeCmd(&cfg), newAdminCmd(&cfg))
	return root
}
