# 🎵 go-navidrome-sync

> ⚠️ **AI-assisted project.** This codebase was developed with significant AI assistance. It has not been exhaustively audited by a human. Use with caution, especially in production environments or against a music library you cannot easily restore.

`go-navidrome-sync` reads ratings, play statistics, starred state, and playlists from local files, matches tracks in Navidrome, and syncs metadata either to Navidrome or back to the local library.

It talks to Navidrome through the Subsonic API for ratings, search, and scrobbling, with native Navidrome search support for richer metadata where available.

## How it works

The sync flow is:

1. Scan a local music directory for supported audio files (`.mp3`, `.flac`, `.ogg`, `.oga`, `.opus`, `.m4a`, `.aac`, `.mp4`).
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

The default config file is `config.yaml`. Pass a different file with `-c /path/to/config.yaml`.

Copy `config.example.yaml` to `config.yaml` and adjust it for your environment.

If `config.yaml` is missing, the tool still runs from environment variables and command-line flags alone.

Every config key can be set as an environment variable by uppercasing and prefixing with `APP_`, replacing `.` with `_`. For example, `navidrome.password` → `APP_NAVIDROME_PASSWORD`.

### Full config reference

```yaml
# Log verbosity. One of: debug, info, warn, error. Default: info.
loglevel: info

navidrome:
  # Required. Base URL of your Navidrome instance.
  baseurl: "https://your-navidrome.example.com"
  # Required. Navidrome username.
  user: "your-user"
  # Required. Navidrome password.
  password: "your-password"
  # Skip TLS certificate verification. Default: false.
  tlsskipverify: false

sync:
  # Required. Path to the local music library root.
  musicpath: "/path/to/music"
  # Conflict resolution when both sides have a rating and they differ.
  # "local"  — local file wins (default)
  # "navidrome" — Navidrome wins
  prefer: "local"
  # Strip this prefix from Navidrome track paths before comparing with local
  # relative paths. Useful when Navidrome serves files from a network share
  # mounted at a different path than the local library root.
  # Example: "/share/Music"
  remotepathprefix: ""
  # Minimum delay between Subsonic search API calls.
  # Increase if Navidrome rate-limits searches. Use "0s" to disable.
  # Default: "100ms"
  searchinterval: "100ms"
  # Choose which metadata categories to sync. If all three are false, ratings
  # and playstats are enabled by default.
  metadata:
    ratings: true    # sync star ratings (1–5)
    playstats: true  # sync play count and last-played timestamp
    stars: false     # sync starred/favorite state (opt-in)
  stars:
    # Conflict resolution for starred state, when both sides differ.
    # Same values as sync.prefer. Falls back to sync.prefer when empty.
    prefer: ""
  # Priority order of rating sources to read from a file. The first source that
  # yields a rating wins. Each source maps to the tag(s) that application writes
  # (and to the right value scale): MediaMonkey -> FMPS_Rating (float 0.0-1.0),
  # foobar2000 -> RATING (int 1-5), WMP -> POPM (non-linear byte scale),
  # iTunes -> POPM / rating (linear 0-100). Sources not present in a file are
  # simply skipped. Default: ["WMP", "iTunes", "MediaMonkey", "foobar2000"].
  ratingtagorder: ["WMP", "iTunes", "MediaMonkey", "foobar2000"]

playlists:
  # Directory to scan for local .m3u / .m3u8 files. Scanned recursively.
  # Default: "./playlists"
  path: "./playlists"
  # Music library root used to resolve relative track paths inside playlists.
  # Defaults to sync.musicpath when empty.
  musicpath: ""
  # Same as sync.remotepathprefix but for playlist track matching.
  # Defaults to sync.remotepathprefix when empty.
  remotepathprefix: ""
  # Conflict resolution when both a local and remote playlist exist with the
  # same name and different contents.
  # "local" — push local playlist to Navidrome (default)
  # "navidrome" — export remote playlist over the local file
  prefer: "local"
  # Format used when writing exported playlists to disk.
  # "m3u8" (default) or "m3u"
  format: "m3u8"
  # Make newly-created remote playlists public. Default: false.
  public: false
  # During sync, delete remote playlists that have no matching local file.
  # Destructive — off by default.
  removemissing: false
  # What to do when a local playlist track cannot be matched to a Navidrome song.
  # "error" — abort the playlist action and report it as an error (default)
  # "skip"  — create/update the playlist with only the matched tracks
  onunmatched: "error"
  # Path style used when writing track paths into exported .m3u files.
  # "relative" — relative to the playlist file's directory (default)
  # "absolute" — absolute path on the local filesystem
  # "remote"   — Navidrome's own path for the track
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

The tool reads and writes ratings, play counts, last-played timestamps, and
starred/favorite state for these containers:

| Container | Extensions | Backend |
|-----------|------------|---------|
| MP3 | `.mp3` | `bogem/id3v2` (ID3v2) |
| FLAC | `.flac` | `mewkiz/flac` (Vorbis comments) |
| Ogg / Opus | `.ogg`, `.oga`, `.opus` | TagLib (Vorbis comments) |
| M4A / AAC / MP4 | `.m4a`, `.aac`, `.mp4` | TagLib (MP4 atoms) |

The Ogg/Opus and M4A paths use [`go.senan.xyz/taglib`](https://pkg.go.dev/go.senan.xyz/taglib),
an embedded WebAssembly build of TagLib, so no `cgo` or system libraries are
required (`CGO_ENABLED=0` builds work).

Ratings are read using the configurable `sync.ratingtagorder` (see the config
reference). MP3 and FLAC retain full per-source fidelity, including
distinguishing WMP from iTunes `POPM` frames by their rater identifier. For the
TagLib-backed formats, TagLib normalizes away the `POPM` rater, so only the
Vorbis/MP4 rating sources (`MediaMonkey` via `FMPS_RATING`, `foobar2000` via
`RATING`) are distinguished.

Playlists are also synced through `.m3u` / `.m3u8` files.

When pushing play statistics to Navidrome, the tool submits scrobbles for the difference between the local and remote play counts. When pulling play statistics from Navidrome, it writes the remote play count and last-played timestamp back to the local tags.

Starred state uses `TXXX:FAVORITE=1` for MP3 files and `FAVORITE=1` for Vorbis comments / MP4 atoms.

> **Note:** the rating value mapping for FMPS_Rating now uses a ceiling-based
> curve (0.2/0.4/0.6/0.8/1.0 land exactly on 1–5 stars) to match the
> `nd-rating-sync` plugin. Pre-existing libraries rated near a star boundary may
> shift by one star on the next sync.

## Failure behavior

If any rating write, starred-state write, scrobble submission, playlist update, or local tag write fails, the command exits non-zero after logging the per-file errors. This makes the tool safer to use from cron, systemd timers, and other automation.
