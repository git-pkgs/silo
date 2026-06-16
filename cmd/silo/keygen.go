package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/git-pkgs/silo/internal/config"
	"github.com/git-pkgs/silo/internal/signer"
)

func newKeygenCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "keygen",
		Short: "Generate the forge witness key",
		Long: `Generate an ed25519 signing key under $SILO_DATA/forge.key. This key
witnesses RSL entries for every accepted push. Repository policies
include it as a witness principal, not an authoriser; see docs/trust-model.md.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := signer.Generate(cfg.DataDir); err != nil {
				return err
			}
			s, err := signer.Load(cfg.DataDir)
			if err != nil {
				return err
			}
			fmt.Println("wrote forge key", s.ID())
			return nil
		},
	}
}

func newPubkeyCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "pubkey",
		Short: "Print the forge witness public key",
		Long: `Print the forge public key in SSH authorized_keys format, for inclusion
in repository gittuf policies as a witness principal.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			s, err := signer.Load(cfg.DataDir)
			if err != nil {
				return err
			}
			ak, err := signer.AuthorizedKey(s)
			if err != nil {
				return err
			}
			fmt.Print(ak)
			fmt.Println("# fingerprint:", s.ID())
			return nil
		},
	}
}
