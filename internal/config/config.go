// Package config resolves the Fastmail API token from the environment or a
// config file. The token is deliberately not accepted as a command-line flag:
// arguments leak into shell history and into `ps` output.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvVar is the environment variable checked before the config file.
const EnvVar = "FASTMAIL_API_TOKEN"

// ErrNoToken is returned when neither the environment variable nor a config
// file supplies a token.
var ErrNoToken = errors.New("no Fastmail API token found")

// Config holds the resolved credentials and where they came from.
type Config struct {
	Token string
	// Source describes the origin of the token, for --verbose output. It never
	// contains the token itself.
	Source string
}

// Dir returns the directory holding config.toml, honoring XDG_CONFIG_HOME.
func Dir() (string, error) {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "email-archiver"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".config", "email-archiver"), nil
}

// Path returns the full path to the config file.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// Load resolves a token, preferring the environment variable. A config file is
// only read if it is not group- or world-readable.
func Load() (Config, error) {
	if tok := strings.TrimSpace(os.Getenv(EnvVar)); tok != "" {
		return Config{Token: tok, Source: "$" + EnvVar}, nil
	}

	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	tok, err := loadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("%w: set $%s or create %s with `api_token = \"...\"`", ErrNoToken, EnvVar, path)
		}
		return Config{}, err
	}
	return Config{Token: tok, Source: path}, nil
}

// loadFile reads api_token from a one-key TOML-style file. It refuses files
// readable by anyone other than the owner.
func loadFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("%s is readable by others (mode %04o); run: chmod 600 %s", path, perm, path)
	}

	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, ok := parseLine(scanner.Text())
		if ok && key == "api_token" {
			if value == "" {
				return "", fmt.Errorf("%s: api_token is empty", path)
			}
			return value, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return "", fmt.Errorf("%s: no api_token key found", path)
}

// parseLine handles `key = "value"`, `key = value`, comments, and blank lines.
func parseLine(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	key, value, ok = strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
	}
	return key, value, true
}
