package playlist

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/log"
	"github.com/rigerc/go-navidrome-sync/internal/navidrome"
)

// stubClient implements Client for testing.
type stubClient struct {
	playlists     []navidrome.RemotePlaylist
	playlistByID  map[string]*navidrome.RemotePlaylist
	searchResults map[string][]*navidrome.RemoteSong
	created       []string
	replaced      []string
	deleted       []string
}

func (s *stubClient) Playlists(_ context.Context) ([]navidrome.RemotePlaylist, error) {
	return s.playlists, nil
}

func (s *stubClient) Playlist(_ context.Context, id string) (*navidrome.RemotePlaylist, error) {
	if s.playlistByID != nil {
		return s.playlistByID[id], nil
	}
	return nil, nil
}

func (s *stubClient) CreatePlaylist(_ context.Context, name string, _ []string) (*navidrome.RemotePlaylist, error) {
	s.created = append(s.created, name)
	p := &navidrome.RemotePlaylist{ID: "new-" + name, Name: name}
	return p, nil
}

func (s *stubClient) ReplacePlaylist(_ context.Context, id string, _ []string) (*navidrome.RemotePlaylist, error) {
	s.replaced = append(s.replaced, id)
	return &navidrome.RemotePlaylist{ID: id}, nil
}

func (s *stubClient) UpdatePlaylist(_ context.Context, _ string, _ *bool) error {
	return nil
}

func (s *stubClient) DeletePlaylist(_ context.Context, id string) error {
	s.deleted = append(s.deleted, id)
	return nil
}

func (s *stubClient) SearchSongsByTitle(_ context.Context, title string, _ int) ([]*navidrome.RemoteSong, error) {
	return s.searchResults[title], nil
}

func testConfig(dir string) Config {
	return Config{
		Path:        dir,
		MusicPath:   dir,
		Prefer:      "local",
		OnUnmatched: "skip",
		ExportPaths: "relative",
	}
}

func writeM3U(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	return path
}

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	writeM3U(t, dir, "alpha.m3u8", "#EXTM3U\n")
	writeM3U(t, dir, "beta.m3u", "#EXTM3U\n")
	writeM3U(t, dir, "ignore.txt", "not a playlist")

	locals, err := LoadDir(dir, dir)
	if err != nil {
		t.Fatalf("LoadDir() error = %v", err)
	}
	if len(locals) != 2 {
		t.Fatalf("len(locals) = %d, want 2", len(locals))
	}
	if locals[0].Name != "alpha" || locals[1].Name != "beta" {
		t.Errorf("names = %q %q, want alpha beta", locals[0].Name, locals[1].Name)
	}
}

func TestLoadDir_UsesPlaylistDirective(t *testing.T) {
	dir := t.TempDir()
	writeM3U(t, dir, "file.m3u8", "#EXTM3U\n#PLAYLIST:My Custom Name\n")

	locals, err := LoadDir(dir, dir)
	if err != nil {
		t.Fatalf("LoadDir() error = %v", err)
	}
	if len(locals) != 1 {
		t.Fatalf("len(locals) = %d, want 1", len(locals))
	}
	if locals[0].Name != "My Custom Name" {
		t.Errorf("Name = %q, want %q", locals[0].Name, "My Custom Name")
	}
}

func TestBuildPlan_CreateRemote(t *testing.T) {
	dir := t.TempDir()
	writeM3U(t, dir, "new.m3u8", "#EXTM3U\n")

	locals, err := LoadDir(dir, dir)
	if err != nil {
		t.Fatalf("LoadDir() error = %v", err)
	}

	client := &stubClient{playlists: nil}
	cfg := testConfig(dir)
	plan, err := BuildPlan(context.Background(), client, cfg, locals)
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}

	if len(plan.Actions) != 1 {
		t.Fatalf("len(plan.Actions) = %d, want 1", len(plan.Actions))
	}
	if plan.Actions[0].Action != ActionCreateRemote {
		t.Errorf("action = %q, want %q", plan.Actions[0].Action, ActionCreateRemote)
	}
	if plan.Actions[0].Name != "new" {
		t.Errorf("name = %q, want %q", plan.Actions[0].Name, "new")
	}
}

func TestBuildPlan_SkipWhenIdentical(t *testing.T) {
	dir := t.TempDir()
	writeM3U(t, dir, "existing.m3u8", "#EXTM3U\n")

	locals, err := LoadDir(dir, dir)
	if err != nil {
		t.Fatalf("LoadDir() error = %v", err)
	}

	remote := navidrome.RemotePlaylist{ID: "r1", Name: "existing", Entry: []navidrome.Song{}}
	client := &stubClient{
		playlists:    []navidrome.RemotePlaylist{remote},
		playlistByID: map[string]*navidrome.RemotePlaylist{"r1": &remote},
	}
	cfg := testConfig(dir)
	plan, err := BuildPlan(context.Background(), client, cfg, locals)
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}

	if len(plan.Actions) != 1 {
		t.Fatalf("len(plan.Actions) = %d, want 1", len(plan.Actions))
	}
	if plan.Actions[0].Action != ActionSkip {
		t.Errorf("action = %q, want %q", plan.Actions[0].Action, ActionSkip)
	}
}

func TestBuildPlan_UnmatchedTracksError(t *testing.T) {
	dir := t.TempDir()
	// Write a music file so the track path can resolve
	trackPath := filepath.Join(dir, "song.mp3")
	if err := os.WriteFile(trackPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	writeM3U(t, dir, "pl.m3u8", "#EXTM3U\n#EXTINF:0,Song\nsong.mp3\n")

	locals, err := LoadDir(dir, dir)
	if err != nil {
		t.Fatalf("LoadDir() error = %v", err)
	}

	// Search returns nothing → unmatched track
	client := &stubClient{playlists: nil, searchResults: map[string][]*navidrome.RemoteSong{}}
	cfg := testConfig(dir)
	cfg.OnUnmatched = "error"

	plan, err := BuildPlan(context.Background(), client, cfg, locals)
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}

	if len(plan.Actions) != 1 {
		t.Fatalf("len(plan.Actions) = %d, want 1", len(plan.Actions))
	}
	if plan.Actions[0].Action != ActionError {
		t.Errorf("action = %q, want %q", plan.Actions[0].Action, ActionError)
	}
}

func TestApply_DryRun_DoesNotMutate(t *testing.T) {
	client := &stubClient{}
	cfg := testConfig(t.TempDir())
	logger := log.NewWithOptions(os.Stderr, log.Options{Level: log.ErrorLevel})

	plan := &Plan{Actions: []PlannedAction{
		{Action: ActionCreateRemote, Name: "pl", SongIDs: []string{"s1"}},
	}}

	if err := Apply(context.Background(), client, cfg, plan, true, logger); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(client.created) != 0 {
		t.Errorf("CreatePlaylist called %d times in dry-run, want 0", len(client.created))
	}
}

func TestApply_ErrorActionAborts(t *testing.T) {
	client := &stubClient{}
	cfg := testConfig(t.TempDir())
	logger := log.NewWithOptions(os.Stderr, log.Options{Level: log.ErrorLevel})

	plan := &Plan{Actions: []PlannedAction{
		{Action: ActionError, Name: "pl", Message: "unmatched tracks: song.mp3"},
	}}

	err := Apply(context.Background(), client, cfg, plan, false, logger)
	if err == nil {
		t.Fatal("Apply() expected error for ActionError, got nil")
	}
}

func TestSafeFilename(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"normal", "normal"},
		{"with/slash", "with_slash"},
		{`back\slash`, "back_slash"},
		{"colon:here", "colon_here"},
		{`question?mark`, "question_mark"},
		{"  trimmed  ", "trimmed"},
		{"", "playlist"},
	}
	for _, tc := range cases {
		got := safeFilename(tc.input)
		if got != tc.want {
			t.Errorf("safeFilename(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"valid local", Config{Prefer: "local", OnUnmatched: "error", ExportPaths: "relative"}, false},
		{"valid navidrome", Config{Prefer: "navidrome", OnUnmatched: "skip", ExportPaths: "absolute"}, false},
		{"invalid prefer", Config{Prefer: "both", OnUnmatched: "error", ExportPaths: "relative"}, true},
		{"invalid onunmatched", Config{Prefer: "local", OnUnmatched: "warn", ExportPaths: "relative"}, true},
		{"invalid exportpaths", Config{Prefer: "local", OnUnmatched: "error", ExportPaths: "symlink"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
