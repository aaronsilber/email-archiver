package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aaronsilber/email-archiver/internal/config"
)

// writeConfig creates a config file with the given contents and mode inside a
// temp XDG_CONFIG_HOME.
func writeConfig(t *testing.T, contents string, mode os.FileMode) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv(config.EnvVar, "")

	dir := filepath.Join(base, "email-archiver")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadPrefersEnvironment(t *testing.T) {
	writeConfig(t, "api_token = \"from-file\"\n", 0o600)
	t.Setenv(config.EnvVar, "from-env")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Token != "from-env" {
		t.Errorf("token = %q, want the environment value", cfg.Token)
	}
	if !strings.Contains(cfg.Source, config.EnvVar) {
		t.Errorf("source = %q, want it to name the environment variable", cfg.Source)
	}
	if strings.Contains(cfg.Source, "from-env") {
		t.Error("source leaks the token")
	}
}

func TestLoadFromFile(t *testing.T) {
	path := writeConfig(t, "# Fastmail\napi_token = \"secret-token\"\n", 0o600)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Token != "secret-token" {
		t.Errorf("token = %q, want secret-token", cfg.Token)
	}
	if cfg.Source != path {
		t.Errorf("source = %q, want %q", cfg.Source, path)
	}
}

func TestLoadAcceptsUnquotedValue(t *testing.T) {
	writeConfig(t, "api_token = secret-token\n", 0o600)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Token != "secret-token" {
		t.Errorf("token = %q, want secret-token", cfg.Token)
	}
}

// TestLoadRefusesWorldReadableFile keeps a credential file from sitting on
// disk where every process on the machine can read it.
func TestLoadRefusesWorldReadableFile(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604} {
		path := writeConfig(t, "api_token = \"secret\"\n", mode)

		_, err := config.Load()
		if err == nil {
			t.Fatalf("Load accepted a config file with mode %04o", mode)
		}
		if !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("error %q should tell the user how to fix it", err)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Error("error message leaks the token")
		}
		_ = path
	}
}

func TestLoadWithNoTokenAnywhere(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv(config.EnvVar, "")

	_, err := config.Load()
	if !errors.Is(err, config.ErrNoToken) {
		t.Fatalf("error = %v, want ErrNoToken", err)
	}
	if !strings.Contains(err.Error(), config.EnvVar) {
		t.Errorf("error %q should say where to put the token", err)
	}
}

func TestLoadRejectsEmptyAndMissingKey(t *testing.T) {
	writeConfig(t, "api_token = \"\"\n", 0o600)
	if _, err := config.Load(); err == nil {
		t.Error("Load accepted an empty api_token")
	}

	writeConfig(t, "# nothing here\n", 0o600)
	if _, err := config.Load(); err == nil {
		t.Error("Load accepted a config file with no api_token")
	}
}

func TestPathHonorsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	path, err := config.Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if want := "/custom/config/email-archiver/config.toml"; path != want {
		t.Errorf("Path = %q, want %q", path, want)
	}
}
