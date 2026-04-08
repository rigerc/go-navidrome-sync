package cmd

import (
	"testing"

	"github.com/rigerc/go-navidrome-ratings-sync/internal/config"
)

func TestApplySyncOverrides_AllowsFlagOnlyConfig(t *testing.T) {
	oldPreferFlag := preferFlag
	oldUserFlag := userFlag
	oldPassFlag := passFlag
	oldBaseURLFlag := baseURLFlag
	oldRemotePathPrefix := remotePathPrefix
	oldTLSSkipFlag := tlsSkipFlag
	t.Cleanup(func() {
		preferFlag = oldPreferFlag
		userFlag = oldUserFlag
		passFlag = oldPassFlag
		baseURLFlag = oldBaseURLFlag
		remotePathPrefix = oldRemotePathPrefix
		tlsSkipFlag = oldTLSSkipFlag
	})

	preferFlag = "navidrome"
	userFlag = "alice"
	passFlag = "secret"
	baseURLFlag = "https://navidrome.example"
	remotePathPrefix = "/music"
	tlsSkipFlag = true

	cfg := &config.Config{}
	applySyncOverrides(cfg, []string{"/library"})

	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if cfg.Sync.MusicPath != "/library" {
		t.Fatalf("cfg.Sync.MusicPath = %q, want %q", cfg.Sync.MusicPath, "/library")
	}
	if cfg.Sync.Prefer != "navidrome" {
		t.Fatalf("cfg.Sync.Prefer = %q, want %q", cfg.Sync.Prefer, "navidrome")
	}
	if cfg.Navidrome.BaseURL != "https://navidrome.example" {
		t.Fatalf("cfg.Navidrome.BaseURL = %q, want %q", cfg.Navidrome.BaseURL, "https://navidrome.example")
	}
	if !cfg.Navidrome.TLSSkipVerify {
		t.Fatal("cfg.Navidrome.TLSSkipVerify = false, want true")
	}
}
