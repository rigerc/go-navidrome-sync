package playlist

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/charmbracelet/log"
	m3u "github.com/sherif-fanous/m3u"

	"github.com/rigerc/go-navidrome-ratings-sync/internal/navidrome"
)

var (
	filenameReplacer = strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_",
		"?", "_", `"`, "_", "<", "_", ">", "_", "|", "_",
	)
	trackPrefixRe = regexp.MustCompile(`^(\d+(?:-\d+)?)(?:\s*-\s*|\s+)`)
)

type Config struct {
	Path             string
	MusicPath        string
	RemotePathPrefix string
	Prefer           string
	Public           bool
	RemoveMissing    bool
	OnUnmatched      string
	ExportPaths      string
}

func (c Config) Validate() error {
	if c.Prefer != "local" && c.Prefer != "navidrome" {
		return fmt.Errorf("playlist prefer must be %q or %q, got %q", "local", "navidrome", c.Prefer)
	}
	if c.OnUnmatched != "error" && c.OnUnmatched != "skip" {
		return fmt.Errorf("playlist on-unmatched must be %q or %q, got %q", "error", "skip", c.OnUnmatched)
	}
	if c.ExportPaths != "relative" && c.ExportPaths != "absolute" && c.ExportPaths != "remote" {
		return fmt.Errorf("playlist export-paths must be %q, %q, or %q, got %q", "relative", "absolute", "remote", c.ExportPaths)
	}
	return nil
}

type Local struct {
	Name    string
	Path    string
	Tracks  []LocalTrack
	ModTime string
}

type LocalTrack struct {
	Name string
	Path string
	Rel  string
}

type Action string

const (
	ActionCreateRemote  Action = "create_remote"
	ActionReplaceRemote Action = "replace_remote"
	ActionExportLocal   Action = "export_local"
	ActionReplaceLocal  Action = "replace_local"
	ActionDeleteRemote  Action = "delete_remote"
	ActionDeleteLocal   Action = "delete_local"
	ActionSkip          Action = "skip"
	ActionError         Action = "error"
)

type Plan struct {
	Actions []PlannedAction `json:"actions"`
	DryRun  bool            `json:"dry_run"`
}

type PlannedAction struct {
	Action     Action   `json:"action"`
	Name       string   `json:"name"`
	LocalPath  string   `json:"local_path,omitempty"`
	RemoteID   string   `json:"remote_id,omitempty"`
	SongIDs    []string `json:"song_ids,omitempty"`
	Message    string   `json:"message,omitempty"`
	RemoteOnly bool     `json:"remote_only,omitempty"`
	LocalOnly  bool     `json:"local_only,omitempty"`
}

type Client interface {
	Playlists(ctx context.Context) ([]navidrome.RemotePlaylist, error)
	Playlist(ctx context.Context, id string) (*navidrome.RemotePlaylist, error)
	CreatePlaylist(ctx context.Context, name string, songIDs []string) (*navidrome.RemotePlaylist, error)
	ReplacePlaylist(ctx context.Context, playlistID string, songIDs []string) (*navidrome.RemotePlaylist, error)
	UpdatePlaylist(ctx context.Context, playlistID string, public *bool) error
	DeletePlaylist(ctx context.Context, id string) error
	SearchSongsByTitle(ctx context.Context, title string, limit int) ([]*navidrome.RemoteSong, error)
}

func LoadDir(dir, musicPath string) ([]Local, error) {
	var playlists []Local
	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".m3u" && ext != ".m3u8" {
			return nil
		}
		pl, err := ReadLocal(path, musicPath)
		if err != nil {
			return fmt.Errorf("reading playlist %s: %w", path, err)
		}
		playlists = append(playlists, *pl)
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(playlists, func(i, j int) bool { return playlists[i].Name < playlists[j].Name })
	return playlists, nil
}

func ReadLocal(path, musicPath string) (*Local, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	for _, line := range strings.Split(string(data), "\n") {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), "#PLAYLIST:"); ok && strings.TrimSpace(after) != "" {
			name = strings.TrimSpace(after)
			break
		}
	}
	parsed, err := m3u.Unmarshal(normalizeM3U(data))
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, err
	}
	pl := &Local{Name: name, Path: path, ModTime: info.ModTime().Format("2006-01-02T15:04:05Z07:00")}
	for _, track := range parsed.Tracks {
		if track.URL == nil {
			continue
		}
		raw := trackURLPath(track.URL)
		resolved, rel, err := resolveTrackPath(raw, filepath.Dir(absPath), musicPath)
		if err != nil {
			pl.Tracks = append(pl.Tracks, LocalTrack{Name: track.Name, Path: raw})
			continue
		}
		pl.Tracks = append(pl.Tracks, LocalTrack{Name: track.Name, Path: resolved, Rel: filepath.ToSlash(rel)})
	}
	return pl, nil
}

func normalizeM3U(data []byte) []byte {
	var lines []string
	expectsPath := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line == "#EXTM3U" || strings.HasPrefix(line, "#PLAYLIST:") || strings.HasPrefix(line, "#EXTALBUMARTURL:") {
			continue
		}
		if strings.HasPrefix(line, "#EXTINF:") {
			lines = append(lines, line)
			expectsPath = true
			continue
		}
		if strings.HasPrefix(line, "#") {
			if expectsPath {
				lines = append(lines, line)
			}
			continue
		}
		if !expectsPath {
			lines = append(lines, "#EXTINF:-1,"+strings.TrimSuffix(filepath.Base(line), filepath.Ext(line)))
		}
		lines = append(lines, line)
		expectsPath = false
	}
	return []byte("#EXTM3U\n" + strings.Join(lines, "\n") + "\n")
}

func trackURLPath(u *url.URL) string {
	if u.Scheme == "file" {
		return u.Path
	}
	if u.Scheme != "" && u.Scheme != "file" {
		return u.String()
	}
	if u.Path != "" {
		return u.Path
	}
	return u.String()
}

func WriteLocal(path, name, musicPath, exportPaths string, remotePathPrefix string, entries []navidrome.Song) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	pl := &m3u.Playlist{}
	for _, song := range entries {
		trackPath := exportPath(song.Path, musicPath, remotePathPrefix, exportPaths)
		u := &url.URL{Path: filepath.ToSlash(trackPath)}
		pl.Tracks = append(pl.Tracks, m3u.Track{Length: 0, Name: song.Artist + " - " + song.ID, URL: u})
	}
	data, err := m3u.Marshal(pl, m3u.M3U)
	if err != nil {
		return err
	}
	content := "#EXTM3U\n#PLAYLIST:" + name + "\n" + strings.TrimPrefix(string(data), "#EXTM3U\n")
	return os.WriteFile(path, []byte(content), 0o644)
}

func BuildPlan(ctx context.Context, client Client, cfg Config, locals []Local) (*Plan, error) {
	remoteList, err := client.Playlists(ctx)
	if err != nil {
		return nil, err
	}
	remoteByName := map[string]navidrome.RemotePlaylist{}
	duplicateRemoteNames := map[string]bool{}
	for _, remote := range remoteList {
		if _, exists := remoteByName[remote.Name]; exists {
			duplicateRemoteNames[remote.Name] = true
			continue
		}
		remoteByName[remote.Name] = remote
	}
	localByName := map[string]Local{}
	for _, local := range locals {
		localByName[local.Name] = local
	}

	plan := &Plan{}
	for _, local := range locals {
		if duplicateRemoteNames[local.Name] {
			plan.Actions = append(plan.Actions, PlannedAction{Action: ActionError, Name: local.Name, LocalPath: local.Path, Message: "multiple remote playlists have this name"})
			continue
		}
		remote, exists := remoteByName[local.Name]
		songIDs, unresolved, err := localSongIDs(ctx, client, cfg, local)
		if err != nil {
			return nil, err
		}
		if len(unresolved) > 0 && cfg.OnUnmatched == "error" {
			plan.Actions = append(plan.Actions, PlannedAction{Action: ActionError, Name: local.Name, LocalPath: local.Path, Message: "unmatched tracks: " + strings.Join(unresolved, ", ")})
			continue
		}
		if !exists {
			plan.Actions = append(plan.Actions, PlannedAction{Action: ActionCreateRemote, Name: local.Name, LocalPath: local.Path, SongIDs: songIDs, LocalOnly: true})
			continue
		}
		full, err := client.Playlist(ctx, remote.ID)
		if err != nil {
			return nil, err
		}
		if full != nil {
			remote = *full
		}
		if remote.Readonly {
			plan.Actions = append(plan.Actions, PlannedAction{Action: ActionError, Name: local.Name, RemoteID: remote.ID, Message: "remote playlist is readonly"})
			continue
		}
		if sameIDs(songIDs, entryIDs(remote.Entry)) {
			plan.Actions = append(plan.Actions, PlannedAction{Action: ActionSkip, Name: local.Name, LocalPath: local.Path, RemoteID: remote.ID})
			continue
		}
		if cfg.Prefer == "local" {
			plan.Actions = append(plan.Actions, PlannedAction{Action: ActionReplaceRemote, Name: local.Name, LocalPath: local.Path, RemoteID: remote.ID, SongIDs: songIDs})
		} else {
			plan.Actions = append(plan.Actions, PlannedAction{Action: ActionReplaceLocal, Name: local.Name, LocalPath: local.Path, RemoteID: remote.ID})
		}
	}
	for _, remote := range remoteList {
		if _, exists := localByName[remote.Name]; exists {
			continue
		}
		if cfg.RemoveMissing {
			plan.Actions = append(plan.Actions, PlannedAction{Action: ActionDeleteRemote, Name: remote.Name, RemoteID: remote.ID, RemoteOnly: true})
		} else {
			plan.Actions = append(plan.Actions, PlannedAction{Action: ActionExportLocal, Name: remote.Name, RemoteID: remote.ID, RemoteOnly: true})
		}
	}
	return plan, nil
}

func Apply(ctx context.Context, client Client, cfg Config, plan *Plan, dryRun bool, logger *log.Logger) error {
	plan.DryRun = dryRun
	for _, action := range plan.Actions {
		if action.Action == ActionSkip {
			continue
		}
		if action.Action == ActionError {
			return fmt.Errorf("playlist %s: %s", action.Name, action.Message)
		}
		if dryRun {
			logger.Info("[DRY-RUN] Would apply playlist action", "action", action.Action, "name", action.Name)
			continue
		}
		switch action.Action {
		case ActionCreateRemote:
			created, err := client.CreatePlaylist(ctx, action.Name, action.SongIDs)
			if err != nil {
				return err
			}
			if cfg.Public && created != nil {
				if err := client.UpdatePlaylist(ctx, created.ID, &cfg.Public); err != nil {
					return err
				}
			}
		case ActionReplaceRemote:
			if _, err := client.ReplacePlaylist(ctx, action.RemoteID, action.SongIDs); err != nil {
				return err
			}
		case ActionExportLocal, ActionReplaceLocal:
			remote, err := client.Playlist(ctx, action.RemoteID)
			if err != nil {
				return err
			}
			if remote == nil {
				return fmt.Errorf("remote playlist %q not found", action.RemoteID)
			}
			path := action.LocalPath
			if path == "" {
				path = filepath.Join(cfg.Path, safeFilename(action.Name)+".m3u8")
			}
			if err := WriteLocal(path, action.Name, cfg.MusicPath, cfg.ExportPaths, cfg.RemotePathPrefix, remote.Entry); err != nil {
				return err
			}
		case ActionDeleteRemote:
			if err := client.DeletePlaylist(ctx, action.RemoteID); err != nil {
				return err
			}
		case ActionDeleteLocal:
			if err := os.Remove(action.LocalPath); err != nil {
				return err
			}
		}
		logger.Info("Applied playlist action", "action", action.Action, "name", action.Name)
	}
	return nil
}

func WriteReport(path string, plan *Plan) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return err
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func localSongIDs(ctx context.Context, client Client, cfg Config, local Local) ([]string, []string, error) {
	ids := make([]string, 0, len(local.Tracks))
	var unresolved []string
	cache := map[string]string{}
	for _, track := range local.Tracks {
		if track.Rel == "" {
			unresolved = append(unresolved, track.Path)
			continue
		}
		if id := cache[track.Rel]; id != "" {
			ids = append(ids, id)
			continue
		}
		want := normalizeRel(track.Rel)
		var matched string
		for _, query := range searchQueries(track.Rel) {
			results, err := client.SearchSongsByTitle(ctx, query, 10)
			if err != nil {
				return nil, nil, err
			}
			for _, result := range results {
				if pathMatches(want, normalizeRel(remoteRelPath(result.Path, cfg))) {
					matched = result.ID
					break
				}
			}
			if matched != "" {
				break
			}
			if len(results) == 1 {
				matched = results[0].ID
				break
			}
		}
		if matched == "" {
			unresolved = append(unresolved, track.Rel)
			continue
		}
		cache[track.Rel] = matched
		ids = append(ids, matched)
	}
	return ids, unresolved, nil
}

func searchQueries(rel string) []string {
	artist, album, title := pathMetadata(rel)
	candidates := []string{
		joinQueryParts(title, artist, album),
		joinQueryParts(title, artist),
		joinQueryParts(title),
	}
	queries := make([]string, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		queries = append(queries, candidate)
	}
	return queries
}

func pathMetadata(rel string) (artist string, album string, title string) {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 0 {
		return "", "", ""
	}
	title = trackTitleFromPathPart(parts[len(parts)-1])
	if len(parts) >= 2 {
		album = parts[len(parts)-2]
	}
	if len(parts) >= 3 {
		artist = parts[len(parts)-3]
	}
	return artist, album, title
}

func trackTitleFromPathPart(pathPart string) string {
	name := strings.TrimSuffix(pathPart, filepath.Ext(pathPart))
	if matches := trackPrefixRe.FindStringSubmatch(name); len(matches) > 0 {
		name = strings.TrimSpace(strings.TrimPrefix(name, matches[0]))
	}
	return name
}

func joinQueryParts(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, " ")
}

func pathMatches(localRel, remoteRel string) bool {
	return localRel == remoteRel || strings.HasSuffix(remoteRel, "/"+localRel)
}

func remoteRelPath(remotePath string, cfg Config) string {
	remoteSlash := filepath.ToSlash(remotePath)
	if cfg.RemotePathPrefix != "" {
		prefix := filepath.ToSlash(strings.TrimRight(cfg.RemotePathPrefix, "/"))
		if rel, ok := strings.CutPrefix(remoteSlash, prefix+"/"); ok {
			return rel
		}
	}
	musicSlash := filepath.ToSlash(strings.TrimRight(cfg.MusicPath, string(filepath.Separator)))
	if rel, ok := strings.CutPrefix(remoteSlash, musicSlash+"/"); ok {
		return rel
	}
	return strings.TrimPrefix(remoteSlash, "/")
}

func resolveTrackPath(raw, baseDir, musicPath string) (string, string, error) {
	value := raw
	if u, err := url.Parse(raw); err == nil && u.Scheme == "file" {
		value, _ = url.PathUnescape(u.Path)
	} else if strings.Contains(raw, "%") {
		if decoded, err := url.PathUnescape(raw); err == nil {
			value = decoded
		}
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return "", "", fmt.Errorf("unsupported URL path %q", raw)
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(baseDir, value)
	}
	value = filepath.Clean(value)
	rel, err := filepath.Rel(musicPath, value)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("path %q is outside music path %q", value, musicPath)
	}
	return value, rel, nil
}

func exportPath(remotePath, musicPath, remotePathPrefix, mode string) string {
	rel := remoteRelPath(remotePath, Config{MusicPath: musicPath, RemotePathPrefix: remotePathPrefix})
	switch mode {
	case "absolute":
		return filepath.Join(musicPath, filepath.FromSlash(rel))
	case "remote":
		return remotePath
	default:
		return rel
	}
}

func entryIDs(entries []navidrome.Song) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	return ids
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func normalizeRel(path string) string {
	return strings.ToLower(filepath.ToSlash(filepath.Clean(path)))
}

func safeFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "playlist"
	}
	return filenameReplacer.Replace(name)
}
