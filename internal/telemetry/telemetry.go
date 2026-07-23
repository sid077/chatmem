package telemetry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const (
	installIDFile = "install_id"
	configFile    = "telemetry.json"
	envVar        = "CHATMEM_TELEMETRY"
)

type Config struct {
	Enabled bool `json:"enabled"`
}

// State reflects the effective telemetry configuration after applying
// the env-var → config-file → default precedence chain.
type State struct {
	InstallID string
	Enabled   bool
	Source    string // "env" | "config" | "default"
}

// Load returns the resolved state and, when needed, creates the install id file.
// dataHome is the app's persistent data directory.
func Load(dataHome string) (State, error) {
	id, err := readOrCreateInstallID(dataHome)
	if err != nil {
		return State{}, err
	}

	if v, ok := os.LookupEnv(envVar); ok {
		enabled := !(v == "0" || strings.EqualFold(v, "false") || strings.EqualFold(v, "off"))
		return State{InstallID: id, Enabled: enabled, Source: "env"}, nil
	}

	cfg, err := readConfig(dataHome)
	switch {
	case err == nil:
		return State{InstallID: id, Enabled: cfg.Enabled, Source: "config"}, nil
	case errors.Is(err, os.ErrNotExist):
		return State{InstallID: id, Enabled: true, Source: "default"}, nil
	default:
		return State{}, err
	}
}

// ConfigExists reports whether a telemetry.json is present. Used by
// `chatmem init` to decide whether this is a first run.
func ConfigExists(dataHome string) bool {
	_, err := os.Stat(filepath.Join(dataHome, configFile))
	return err == nil
}

// SetEnabled writes the config file. Env var still wins on subsequent Load calls.
func SetEnabled(dataHome string, enabled bool) error {
	return writeConfig(dataHome, Config{Enabled: enabled})
}

func readOrCreateInstallID(dataHome string) (string, error) {
	path := filepath.Join(dataHome, installIDFile)
	if b, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(b)), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	id := uuid.NewString()
	if err := os.MkdirAll(dataHome, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o644); err != nil {
		return "", err
	}
	return id, nil
}

func readConfig(dataHome string) (Config, error) {
	f, err := os.Open(filepath.Join(dataHome, configFile))
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	var c Config
	if err := json.NewDecoder(f).Decode(&c); err != nil {
		return Config{}, fmt.Errorf("parse telemetry config: %w", err)
	}
	return c, nil
}

func writeConfig(dataHome string, c Config) error {
	if err := os.MkdirAll(dataHome, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dataHome, configFile), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(c)
}
