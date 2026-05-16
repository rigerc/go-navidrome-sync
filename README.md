# go-navidrome-ratings-sync

`go-navidrome-ratings-sync` reads ratings, play statistics, starred state, and playlists from local files, matches tracks in Navidrome, and syncs metadata either to Navidrome or back to the local library.

It talks to Navidrome through the Subsonic API for ratings, search, and scrobbling, with native Navidrome search support for richer metadata where available.

## How it works

The sync flow is:

1. Scan a local music directory for `.mp3` and `.flac` files.
2. Read local rating, play count, last-played, and identifying metadata from each file.
3. Search Navidrome for matching songs through the Subsonic API.
4. Match local files to remote songs using metadata and path heuristics.
5. Sync ratings in the configured direction when local and remote values differ.
6. Sync play statistics by taking the highest play count and most recent last-played timestamp.
7. Optionally sync starred/favorite state.
8. Sync local `.m3u` / `.m3u8` playlists with Navidrome playlists.

When both sides have a rating or starred-state conflict, the configured preference decides whether local metadata or Navidrome wins. Play statistics are reconciled independently by keeping the larger/newer state.

## Navidrome requirement

This tool depends on Navidrome returning the real filesystem path for tracks via the Subsonic API.

Set this environment variable on your Navidrome instance:

```bash
ND_SUBSONIC_DEFAULTREPORTREALPATH=true
```

Without it, Navidrome may not return the real track path, which breaks path-based matching and makes sync results unreliable.

## Configuration

The default config file is `config.yaml`.

If `config.yaml` is missing, the CLI can still run from environment variables and command-line flags.

Example:

```yaml
loglevel: debug

navidrome:
  baseurl: "https://your-navidrome.example.com"
  user: "your-user"
  password: "your-password"
  tlsskipverify: false

sync:
  musicpath: "/path/to/music"
  prefer: "local"
  remotepathprefix: "/share/Music"
  searchinterval: "100ms"
  metadata:
    ratings: true
    playstats: true
    stars: false
  stars:
    prefer: ""
    tag: "FAVORITE"

playlists:
  path: "./playlists"
  musicpath: ""          # defaults to sync.musicpath
  remotepathprefix: ""   # defaults to sync.remotepathprefix
  prefer: "local"
  format: "m3u8"
  public: false
  removemissing: false
  onunmatched: "error"
  exportpaths: "relative"
```

## Usage

Run a sync with the configured music path:

```bash
go run . sync
```

Override the music path on the command line:

```bash
go run . sync /path/to/music
```

Run without a config file by supplying connection flags directly:

```bash
go run . sync /path/to/music \
  --baseurl https://your-navidrome.example.com \
  --user your-user \
  --password your-password \
  --search-interval 250ms
```

Preview changes without writing ratings:

```bash
go run . sync --dry-run
```

Write a JSON report with matched, unmatched, and ambiguous results:

```bash
go run . sync --dry-run --report-json sync-report.json
```

Enable starred/favorite sync for a run:

```bash
go run . sync --stars --stars-prefer local
```

`sync.searchinterval` and `--search-interval` control the minimum delay between remote search requests. Use `0s` to disable the delay.

## Playlists

Playlist commands sync local `.m3u` / `.m3u8` files with Navidrome playlists. Local playlists are matched to remote playlists by name. Remote smart playlists are read-only and can be exported but not replaced.

```bash
go run . playlists list
go run . playlists export ./playlists
go run . playlists import ./playlists
go run . playlists sync ./playlists --prefer local
go run . playlists sync ./playlists --dry-run --report-json playlists-report.json
```

Playlist import fails on unmatched tracks by default. Use `--on-unmatched skip` to create/update playlists with only matched tracks. `--remove-missing` is destructive: during playlist sync it deletes remote playlists that are not present locally.

Navidrome also has its own playlist auto-import support (`AutoImportPlaylists` / `PlaylistsPath`). If that is enabled, avoid syncing the same playlist folder from both systems unless that is intentional.

## Synced metadata

The tool currently supports:

- Ratings for MP3 and FLAC files.
- Play counts for MP3 and FLAC files.
- Last-played timestamps for MP3 and FLAC files.
- Starred/favorite state for MP3 and FLAC files.
- Playlists through `.m3u` / `.m3u8` files.

When pushing play statistics to Navidrome, the tool submits scrobbles for the difference between the local and remote play counts. When pulling play statistics from Navidrome, it writes the remote play count and last-played timestamp back to the local tags.

Starred state uses `TXXX:FAVORITE=1` for MP3 files and `FAVORITE=1` for FLAC/Vorbis comments.

## Failure behavior

If any rating write, starred-state write, scrobble submission, playlist update, or local tag write fails, the command exits non-zero after logging the per-file errors. This makes the tool safer to use from cron, systemd timers, and other automation.
