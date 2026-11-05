package notion

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const configFile = "notion.json"

// Config is what lives at ~/.local/share/chatmem/notion.json.
// Absence of this file = notion integration disabled.
type Config struct {
	IntegrationToken string           `json:"integration_token"`
	ParentPageID     string           `json:"parent_page_id"`
	WorkspaceID      string           `json:"workspace_id,omitempty"`
	ConnectedAt      time.Time        `json:"connected_at"`
	AutoSynthesize   AutoSynthesizeCfg `json:"auto_synthesize"`
}

type AutoSynthesizeCfg struct {
	Enabled          bool `json:"enabled"`
	IdleMinutes      int  `json:"idle_minutes"`
	MessageThreshold int  `json:"message_threshold"`
	MinMessages      int  `json:"min_messages"`
}

// DefaultAuto returns sensible defaults for a fresh install.
func DefaultAuto() AutoSynthesizeCfg {
	return AutoSynthesizeCfg{
		Enabled:          true,
		IdleMinutes:      10,
		MessageThreshold: 20,
		MinMessages:      4,
	}
}

// LoadConfig reads notion.json. Returns (nil, nil) if the file does not
// exist — callers treat that as "notion disabled".
func LoadConfig(dataHome string) (*Config, error) {
	f, err := os.Open(filepath.Join(dataHome, configFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse notion config: %w", err)
	}
	// Fill in defaults only for auto_synthesize fields the file leaves out,
	// so configs from before a field existed keep working. An explicit
	// "enabled": false must survive — it used to be reset to true whenever
	// the thresholds were absent.
	var present struct {
		Auto *struct {
			Enabled          *bool `json:"enabled"`
			IdleMinutes      *int  `json:"idle_minutes"`
			MessageThreshold *int  `json:"message_threshold"`
			MinMessages      *int  `json:"min_messages"`
		} `json:"auto_synthesize"`
	}
	_ = json.Unmarshal(raw, &present)
	def, a := DefaultAuto(), &c.AutoSynthesize
	p := present.Auto
	if p == nil || p.Enabled == nil {
		a.Enabled = def.Enabled
	}
	if p == nil || p.IdleMinutes == nil || a.IdleMinutes <= 0 {
		a.IdleMinutes = def.IdleMinutes
	}
	if p == nil || p.MessageThreshold == nil || a.MessageThreshold <= 0 {
		a.MessageThreshold = def.MessageThreshold
	}
	if p == nil || p.MinMessages == nil || a.MinMessages <= 0 {
		a.MinMessages = def.MinMessages
	}
	return &c, nil
}

// SaveConfig writes notion.json with mode 0600 (token is a bearer secret).
func SaveConfig(dataHome string, c Config) error {
	if err := os.MkdirAll(dataHome, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dataHome, configFile)
	// Write to temp + rename so a crashed write doesn't leave a partial file.
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(c); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// DeleteConfig removes notion.json entirely (disconnect).
func DeleteConfig(dataHome string) error {
	err := os.Remove(filepath.Join(dataHome, configFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ConfigExists reports whether notion.json is present. Used by callers that
// want to answer "is notion integration currently set up?" without loading.
func ConfigExists(dataHome string) bool {
	_, err := os.Stat(filepath.Join(dataHome, configFile))
	return err == nil
}
