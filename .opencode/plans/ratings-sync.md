# Implementation Plan: go-navidrome-sync

## Summary
Scan MP3 files recursively, read/write POPM ratings, bidirectional sync with Navidrome via Subsonic API.

## Dependencies (done)
- `github.com/bogem/id3v2` - ID3v2 POPM tag read/write
- `github.com/supersonic-app/go-subsonic` - Subsonic API client

## Files to Create/Modify

### 1. `config.yaml` - Updated config
```yaml
loglevel: info
navidrome:
  baseurl: "http://localhost:4533"
  user: ""
  password: ""
sync:
  musicpath: ""
  prefer: "local"  # "local" or "navidrome"
```

### 2. `internal/config/config.go` - Updated config struct
- Remove `Server` section
- Change `navidrome.token` to `navidrome.user` + `navidrome.password`
- Add `Sync.MusicPath` and `Sync.Prefer`
- Add `Validate()` function

### 3. `internal/tag/mp3.go` - POPM read/write
- `ReadPOPMRating(path string) (int, error)` - returns 0-5 star rating
- `WritePOPMRating(path string, rating int) error` - writes 0-5 star rating
- POPM mapping: 0=unrated, 1=★, 64=★★, 128=★★★, 196=★★★★, 255=★★★★★

### 4. `internal/navidrome/client.go` - Targeted fetch
- `Connect(cfg) (*subsonic.Client, error)` - create and authenticate client
- `RemoteSong` struct: `{ID, Path, UserRating string}`
- `FetchSongsForPaths(client, localPaths []string) (map[string]RemoteSong, error)` - targeted traversal

### 5. `internal/sync/sync.go` - Core sync logic
- `LocalRating` struct: `{Path string, Rating int}`
- `SyncAction` enum: Push, Pull, Skip
- `SyncResult` struct: `{Action, Path, OldRating, NewRating}`
- `Run(localDir string, remoteSongs map[string]RemoteSong, prefer string, dryRun bool) ([]SyncResult, error)`

### 6. `cmd/root.go` - Updated root command
- Setup slog logger with level from config
- Remove server config references

### 7. `cmd/sync.go` - Sync subcommand
- `sync` subcommand with `--dry-run`, `--prefer`, `--user`, `--password`, `--baseurl` flags
- Positional arg for music path (overrides config)

## Sync Algorithm
1. Walk musicpath for .mp3 files, read POPM ratings
2. Extract unique artist names from paths
3. GetMusicFolders() → GetMusicDirectory() → match artist names
4. For each matched artist, GetMusicDirectory(artistID) → match album names
5. For each matched album, GetMusicDirectory(albumID) → match songs by path
6. Diff: push local→remote, pull remote→local, resolve conflicts with prefer setting

## Logging
- slog with level from config
- debug: file scanning, API calls, path matching, rating comparisons
- info: action summary (pushed/pulled/skipped counts)
- warn: unmatched files, tag errors
- error: API errors, auth failures

## CLI
```
go-navidrome-sync sync [flags] [music-path]
```
Flags: --config, --dry-run, --prefer, --user, --password, --baseurl
