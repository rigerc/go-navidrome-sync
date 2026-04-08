# go-navidrome-ratings-sync

`go-navidrome-ratings-sync` reads ratings from local MP3 and FLAC files, matches those tracks in Navidrome, and syncs ratings either to Navidrome or back to the local files.

It talks to Navidrome through the Subsonic API.

## How it works

The sync flow is:

1. Scan a local music directory for `.mp3` and `.flac` files.
2. Read local rating metadata from each file.
3. Search Navidrome for matching songs through the Subsonic API.
4. Match local files to remote songs using metadata and path heuristics.
5. Sync ratings in the configured direction when local and remote values differ.

When both sides have a rating and they conflict, the configured preference decides whether the local rating or the Navidrome rating wins.

## Navidrome requirement

This tool depends on Navidrome returning the real filesystem path for tracks via the Subsonic API.

Set this environment variable on your Navidrome instance:

```bash
ND_SUBSONIC_DEFAULTREPORTREALPATH=true
```

Without it, Navidrome may not return the real track path, which breaks path-based matching and makes sync results unreliable.

## Configuration

The default config file is `config.yaml`.

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

Preview changes without writing ratings:

```bash
go run . sync --dry-run
```
