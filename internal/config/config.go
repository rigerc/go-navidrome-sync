package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

type Config struct {
	LogLevel  string `koanf:"loglevel"`
	Navidrome struct {
		BaseURL       string `koanf:"baseurl"`
		User          string `koanf:"user"`
		Password      string `koanf:"password"`
		TLSSkipVerify bool   `koanf:"tlsskipverify"`
	} `koanf:"navidrome"`
	Sync struct {
		MusicPath        string `koanf:"musicpath"`
		Prefer           string `koanf:"prefer"`
		RemotePathPrefix string `koanf:"remotepathprefix"`
		SearchInterval   string `koanf:"searchinterval"`
		Metadata         struct {
			Ratings   bool `koanf:"ratings"`
			PlayStats bool `koanf:"playstats"`
			Stars     bool `koanf:"stars"`
		} `koanf:"metadata"`
		Stars struct {
			Prefer string `koanf:"prefer"`
			Tag    string `koanf:"tag"`
		} `koanf:"stars"`
	} `koanf:"sync"`
	Playlists struct {
		Path             string `koanf:"path"`
		MusicPath        string `koanf:"musicpath"`
		RemotePathPrefix string `koanf:"remotepathprefix"`
		Prefer           string `koanf:"prefer"`
		Format           string `koanf:"format"`
		Public           bool   `koanf:"public"`
		RemoveMissing    bool   `koanf:"removemissing"`
		OnUnmatched      string `koanf:"onunmatched"`
		ExportPaths      string `koanf:"exportpaths"`
	} `koanf:"playlists"`
}

const (
	DefaultConfigPath = "config.yaml"
	prefix            = ""
	envPrefix         = "APP_"
)

func Load(configPath string) (*Config, error) {
	k := koanf.New(prefix)

	optionalFile := configPath == ""
	if configPath == "" {
		configPath = DefaultConfigPath
	}

	if err := loadConfigFile(k, configPath, optionalFile); err != nil {
		return nil, fmt.Errorf("failed to load config file %s: %w", configPath, err)
	}

	if err := k.Load(env.Provider(envPrefix, ".", func(s string) string {
		return strings.Replace(
			strings.ToLower(
				strings.TrimPrefix(s, envPrefix),
			),
			"_", ".", -1,
		)
	}), nil); err != nil {
		return nil, fmt.Errorf("failed to load env vars: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}
	ApplyDefaults(&cfg)

	return &cfg, nil
}

func loadConfigFile(k *koanf.Koanf, configPath string, optional bool) error {
	if err := k.Load(file.Provider(configPath), yaml.Parser()); err != nil {
		if optional && os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}

func ApplyDefaults(cfg *Config) {
	if cfg.Sync.Prefer == "" {
		cfg.Sync.Prefer = "local"
	}
	if cfg.Sync.SearchInterval == "" {
		cfg.Sync.SearchInterval = "100ms"
	}
	if !cfg.Sync.Metadata.Ratings && !cfg.Sync.Metadata.PlayStats && !cfg.Sync.Metadata.Stars {
		cfg.Sync.Metadata.Ratings = true
		cfg.Sync.Metadata.PlayStats = true
	}
	if cfg.Sync.Stars.Tag == "" {
		cfg.Sync.Stars.Tag = "FAVORITE"
	}
	if cfg.Playlists.Path == "" {
		cfg.Playlists.Path = "./playlists"
	}
	if cfg.Playlists.Prefer == "" {
		cfg.Playlists.Prefer = "local"
	}
	if cfg.Playlists.Format == "" {
		cfg.Playlists.Format = "m3u8"
	}
	if cfg.Playlists.OnUnmatched == "" {
		cfg.Playlists.OnUnmatched = "error"
	}
	if cfg.Playlists.ExportPaths == "" {
		cfg.Playlists.ExportPaths = "relative"
	}
	if cfg.Playlists.MusicPath == "" {
		cfg.Playlists.MusicPath = cfg.Sync.MusicPath
	}
	if cfg.Playlists.RemotePathPrefix == "" {
		cfg.Playlists.RemotePathPrefix = cfg.Sync.RemotePathPrefix
	}
}

func Validate(cfg *Config) error {
	ApplyDefaults(cfg)

	if cfg.Navidrome.BaseURL == "" {
		return fmt.Errorf("navidrome.baseurl is required")
	}
	if cfg.Navidrome.User == "" {
		return fmt.Errorf("navidrome.user is required")
	}
	if cfg.Navidrome.Password == "" {
		return fmt.Errorf("navidrome.password is required")
	}
	prefer := cfg.Sync.Prefer
	if prefer != "local" && prefer != "navidrome" {
		return fmt.Errorf("sync.prefer must be \"local\" or \"navidrome\", got %q", prefer)
	}
	if cfg.Sync.Stars.Prefer != "" && cfg.Sync.Stars.Prefer != "local" && cfg.Sync.Stars.Prefer != "navidrome" {
		return fmt.Errorf("sync.stars.prefer must be \"local\" or \"navidrome\", got %q", cfg.Sync.Stars.Prefer)
	}
	if cfg.Playlists.Prefer != "local" && cfg.Playlists.Prefer != "navidrome" {
		return fmt.Errorf("playlists.prefer must be \"local\" or \"navidrome\", got %q", cfg.Playlists.Prefer)
	}
	if cfg.Playlists.OnUnmatched != "error" && cfg.Playlists.OnUnmatched != "skip" {
		return fmt.Errorf("playlists.onunmatched must be \"error\" or \"skip\", got %q", cfg.Playlists.OnUnmatched)
	}
	if cfg.Playlists.ExportPaths != "relative" && cfg.Playlists.ExportPaths != "absolute" && cfg.Playlists.ExportPaths != "remote" {
		return fmt.Errorf("playlists.exportpaths must be \"relative\", \"absolute\", or \"remote\", got %q", cfg.Playlists.ExportPaths)
	}
	if _, err := ParseSearchInterval(cfg.Sync.SearchInterval); err != nil {
		return fmt.Errorf("sync.searchinterval is invalid: %w", err)
	}
	return nil
}

func ParseSearchInterval(raw string) (time.Duration, error) {
	interval, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, err
	}
	if interval < 0 {
		return 0, fmt.Errorf("must be >= 0")
	}
	return interval, nil
}
