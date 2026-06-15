// Package config holds runtime configuration loaded from flags and SILO_*
// environment variables.
package config

import "os"

// Config is the resolved runtime configuration for a silo process.
type Config struct {
	DataDir  string
	HTTPAddr string
	SSHAddr  string
	BaseURL  string
}

// Default returns the configuration used when nothing is overridden.
func Default() Config {
	return Config{
		DataDir:  "./silo-data",
		HTTPAddr: ":8080",
		SSHAddr:  ":2222",
		BaseURL:  "http://localhost:8080",
	}
}

// FromEnv returns Default() with any SILO_* environment variables applied.
// Flag parsing in cmd/silo applies on top of this.
func FromEnv() Config {
	c := Default()
	if v := os.Getenv("SILO_DATA"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("SILO_HTTP"); v != "" {
		c.HTTPAddr = v
	}
	if v := os.Getenv("SILO_SSH"); v != "" {
		c.SSHAddr = v
	}
	if v := os.Getenv("SILO_BASE_URL"); v != "" {
		c.BaseURL = v
	}
	return c
}
