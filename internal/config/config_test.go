package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad_DoesNotLeakPreviousValuesBetweenCalls(t *testing.T) {
	root := t.TempDir()

	firstPath := filepath.Join(root, "first.yaml")
	if err := os.WriteFile(firstPath, []byte(`
navidrome:
  baseurl: "https://first.example"
  user: "alice"
  password: "secret"
sync:
  prefer: "local"
`), 0o644); err != nil {
		t.Fatalf("WriteFile(first) error = %v", err)
	}

	secondPath := filepath.Join(root, "second.yaml")
	if err := os.WriteFile(secondPath, []byte(`
navidrome:
  user: "bob"
`), 0o644); err != nil {
		t.Fatalf("WriteFile(second) error = %v", err)
	}

	firstCfg, err := Load(firstPath)
	if err != nil {
		t.Fatalf("Load(first) error = %v", err)
	}
	if firstCfg.Navidrome.BaseURL != "https://first.example" {
		t.Fatalf("firstCfg.Navidrome.BaseURL = %q, want %q", firstCfg.Navidrome.BaseURL, "https://first.example")
	}

	secondCfg, err := Load(secondPath)
	if err != nil {
		t.Fatalf("Load(second) error = %v", err)
	}
	if secondCfg.Navidrome.BaseURL != "" {
		t.Fatalf("secondCfg.Navidrome.BaseURL = %q, want empty", secondCfg.Navidrome.BaseURL)
	}
	if secondCfg.Navidrome.Password != "" {
		t.Fatalf("secondCfg.Navidrome.Password = %q, want empty", secondCfg.Navidrome.Password)
	}
	if secondCfg.Navidrome.User != "bob" {
		t.Fatalf("secondCfg.Navidrome.User = %q, want %q", secondCfg.Navidrome.User, "bob")
	}
}

func TestLoad_OptionalDefaultConfigCanBeMissing(t *testing.T) {
	root := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir(%q) error = %v", root, err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	t.Setenv("APP_NAVIDROME_BASEURL", "https://env.example")
	t.Setenv("APP_NAVIDROME_USER", "alice")
	t.Setenv("APP_NAVIDROME_PASSWORD", "secret")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") error = %v", err)
	}
	if cfg.Navidrome.BaseURL != "https://env.example" {
		t.Fatalf("cfg.Navidrome.BaseURL = %q, want %q", cfg.Navidrome.BaseURL, "https://env.example")
	}
	if cfg.Navidrome.User != "alice" {
		t.Fatalf("cfg.Navidrome.User = %q, want %q", cfg.Navidrome.User, "alice")
	}
	if cfg.Navidrome.Password != "secret" {
		t.Fatalf("cfg.Navidrome.Password = %q, want %q", cfg.Navidrome.Password, "secret")
	}
	if cfg.Sync.SearchInterval != "100ms" {
		t.Fatalf("cfg.Sync.SearchInterval = %q, want %q", cfg.Sync.SearchInterval, "100ms")
	}
}

func TestParseSearchInterval(t *testing.T) {
	got, err := ParseSearchInterval("250ms")
	if err != nil {
		t.Fatalf("ParseSearchInterval() error = %v", err)
	}
	if got != 250*time.Millisecond {
		t.Fatalf("ParseSearchInterval() = %v, want %v", got, 250*time.Millisecond)
	}
}

func TestParseSearchInterval_RejectsNegativeValues(t *testing.T) {
	if _, err := ParseSearchInterval("-1ms"); err == nil {
		t.Fatal("ParseSearchInterval() error = nil, want invalid negative duration")
	}
}
