package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// Positions inside ruTorrent responses.
	pathIndex = 0 // fls row: relative file path
	nameIndex = 4 // list row: torrent name

	chunkSize = 8192
)

var (
	apiTimeout         = 15 * time.Second
	dialTimeout        = 10 * time.Second
	responseHeaderWait = 15 * time.Second
)

// Commands for the "list" request; ordered exactly like the Python payload.
var listCmds = []string{
	"d.throttle_name=",
	"d.custom=chk-state",
	"d.custom=chk-time",
	"d.custom=sch_ignore",
	`cat="$t.multicall=d.hash=,t.scrape_complete=,cat={#}"`,
	`cat="$t.multicall=d.hash=,t.scrape_incomplete=,cat={#}"`,
	"d.custom=x-pushbullet",
	"cat=$d.views=",
	"d.custom=seedingtime",
	"d.custom=addtime",
}

// Extra cmd for the "fls" request, kept verbatim (single param, as before).
const flsCmd = "f.prioritize_first=&cmd=f.prioritize_last="

// Config holds every tunable. Precedence: flags > config file > defaults.
// Hash is deliberately not persisted: it identifies one download, not a
// setting, so it is supplied per run via -H or the wizard.
type Config struct {
	Server  string `json:"server"`
	Hash    string `json:"-"`
	Output  string `json:"output"`
	Jobs    int    `json:"jobs"`
	Retries int    `json:"retries"`
	Probe   bool   `json:"probe"`
}

// DefaultConfig returns the built-in defaults. Server and hash are deliberately
// empty: the user supplies the server via flags, the wizard, or ruget.json,
// and the hash per run.
func DefaultConfig() Config {
	return Config{
		Server:  "",
		Hash:    "",
		Output:  ".",
		Jobs:    1,
		Retries: 3,
		Probe:   true,
	}
}

// ConfigPath returns the config location next to the executable so the host
// system stays clean: <exe dir>\ruget.json. It falls back to the working
// directory when the executable path cannot be resolved.
func ConfigPath() (string, error) {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "ruget.json"), nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ruget.json"), nil
}

// LoadConfig reads the JSON config at path. A missing file is not an error.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}

	// Decode over defaults so omitted keys keep their default value.
	if err := json.Unmarshal(data, &cfg); err != nil {
		return DefaultConfig(), err
	}
	return cfg, nil
}

// SaveConfig writes the config as indented JSON, creating parent directories.
func SaveConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

// validateEndpoint reports the first missing required field, if any.
func (c Config) validateEndpoint() string {
	if c.Server == "" {
		return "server URL is required"
	}
	if c.Hash == "" {
		return "torrent hash is required"
	}
	return ""
}

// cleanBaseURL reduces a pasted server URL to its base form: it drops the
// fragment (e.g. a trailing "#"), any query string and trailing slashes, so
// "http://host:8081/#" becomes "http://host:8081". Non-URL strings are only
// trimmed of surrounding whitespace and trailing slashes.
func cleanBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimRight(raw, "/")
	}
	u.Fragment = ""
	u.RawFragment = ""
	u.RawQuery = ""
	u.ForceQuery = false
	u.Path = strings.TrimRight(u.Path, "/")
	return strings.TrimRight(u.String(), "/")
}

// normalize applies light cleanup consistent with the Python front-end.
func (c *Config) normalize() {
	c.Server = cleanBaseURL(c.Server)
	c.Hash = strings.ToUpper(strings.TrimSpace(c.Hash))
	if c.Output == "" {
		c.Output = "."
	}
	if c.Jobs < 1 {
		c.Jobs = 1
	}
	if c.Retries < 0 {
		c.Retries = 0
	}
}
