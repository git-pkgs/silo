package config

import "testing"

func TestDefault(t *testing.T) {
	c := Default()
	if c.DataDir == "" || c.HTTPAddr == "" || c.SSHAddr == "" || c.BaseURL == "" {
		t.Errorf("Default has empty field: %+v", c)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("SILO_DATA", "/tmp/x")
	t.Setenv("SILO_HTTP", ":9999")
	t.Setenv("SILO_SSH", ":2299")
	t.Setenv("SILO_BASE_URL", "https://x")
	c := FromEnv()
	if c.DataDir != "/tmp/x" || c.HTTPAddr != ":9999" || c.SSHAddr != ":2299" || c.BaseURL != "https://x" {
		t.Errorf("FromEnv = %+v", c)
	}
}

func TestFromEnv_Unset(t *testing.T) {
	for _, k := range []string{"SILO_DATA", "SILO_HTTP", "SILO_SSH", "SILO_BASE_URL"} {
		t.Setenv(k, "")
	}
	if FromEnv() != Default() {
		t.Errorf("FromEnv with nothing set should equal Default")
	}
}
