// Package config holds ytx's settings file and its secrets (API key, OAuth
// token, OAuth client) in the macOS Keychain. Secrets never go in a file in
// the repo or in config.json.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/zalando/go-keyring"

	"github.com/morethancoder/ytx/internal/apperr"
)

// KeychainService is the Keychain service name every ytx secret is stored under.
const KeychainService = "ytx"

// Secret account names.
const (
	SecretAPIKey      = "api_key"
	SecretOAuthToken  = "oauth_token"  // JSON oauth2.Token
	SecretOAuthClient = "oauth_client" // the Google Cloud OAuth client JSON
)

// DefaultQuotaLimit is the official API's default daily units per project.
const DefaultQuotaLimit = 10000

// Config is ~/.ytx/config.json.
type Config struct {
	// ResearchDir is where `research` and `me learn` write notes when --out is not given.
	ResearchDir string `json:"research_dir,omitempty"`
	Region      string `json:"region,omitempty"`
	Lang        string `json:"lang,omitempty"`
	QuotaLimit  int    `json:"quota_limit,omitempty"`
}

// Keys lists the settable keys with their help, for `config set` and errors.
var Keys = map[string]string{
	"research_dir": "folder where research notes are written (e.g. your vault's research folder)",
	"region":       "default two-letter country code",
	"lang":         "default language code",
	"quota_limit":  "official API daily units (default 10000)",
}

// Home is ytx's state folder: $YTX_HOME, else ~/.ytx.
func Home() string {
	if h := os.Getenv("YTX_HOME"); h != "" {
		return h
	}
	d, err := os.UserHomeDir()
	if err != nil {
		return ".ytx"
	}
	return filepath.Join(d, ".ytx")
}

// Path returns a path inside Home, creating Home when needed.
func Path(parts ...string) string {
	_ = os.MkdirAll(Home(), 0o700)
	return filepath.Join(append([]string{Home()}, parts...)...)
}

func file() string { return Path("config.json") }

// Load reads the config; a missing file is an empty config.
func Load() (*Config, error) {
	c := &Config{}
	b, err := os.ReadFile(file())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("read %s: %w", file(), err)
	}
	return c, nil
}

// Save writes the config.
func (c *Config) Save() error {
	b, _ := json.MarshalIndent(c, "", "  ")
	return writeFileAtomic(file(), append(b, '\n'), 0o600)
}

// Limit is the daily quota limit.
func (c *Config) Limit() int {
	if c.QuotaLimit > 0 {
		return c.QuotaLimit
	}
	return DefaultQuotaLimit
}

// Set changes one key.
func (c *Config) Set(key, value string) error {
	switch key {
	case "research_dir":
		if value != "" {
			value = ExpandHome(value)
		}
		c.ResearchDir = value
	case "region":
		c.Region = strings.ToUpper(value)
	case "lang":
		c.Lang = value
	case "quota_limit":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return apperr.New(apperr.InvalidInput, "quota_limit must be a whole number")
		}
		c.QuotaLimit = n
	default:
		names := make([]string, 0, len(Keys))
		for k := range Keys {
			names = append(names, k)
		}
		sort.Strings(names)
		return apperr.New(apperr.InvalidInput, "unknown config key %q (one of: %s)", key, strings.Join(names, ", "))
	}
	return nil
}

// ExpandHome turns a leading ~ into the home folder.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func writeFileAtomic(path string, b []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// WriteFileAtomic writes a file through a temp file and a rename.
func WriteFileAtomic(path string, b []byte, mode os.FileMode) error {
	return writeFileAtomic(path, b, mode)
}

// ---- secrets

// keychainOff is true when YTX_NO_KEYCHAIN is set (tests, CI): secrets then come only from env.
func keychainOff() bool { return os.Getenv("YTX_NO_KEYCHAIN") != "" }

// Secret reads a Keychain secret ("" when absent).
func Secret(account string) (string, error) {
	if keychainOff() {
		return "", nil
	}
	s, err := keyring.Get(KeychainService, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	return s, err
}

// SetSecret stores a Keychain secret.
func SetSecret(account, value string) error {
	if keychainOff() {
		return apperr.New(apperr.InvalidInput, "the Keychain is off (YTX_NO_KEYCHAIN is set)")
	}
	return keyring.Set(KeychainService, account, value)
}

// DeleteSecret removes a Keychain secret; a missing one is not an error.
func DeleteSecret(account string) error {
	if keychainOff() {
		return nil
	}
	err := keyring.Delete(KeychainService, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// APIKey is YOUTUBE_API_KEY from the environment, else the Keychain. where is
// "env", "keychain" or "".
func APIKey() (key, where string) {
	if k := strings.TrimSpace(os.Getenv("YOUTUBE_API_KEY")); k != "" {
		return k, "env"
	}
	if k, _ := Secret(SecretAPIKey); k != "" {
		return strings.TrimSpace(k), "keychain"
	}
	return "", ""
}
