package sync

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/rigerc/go-navidrome-ratings-sync/internal/navidrome"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/tag"
)

type Action int

const (
	ActionPush Action = iota
	ActionPull
	ActionSkip
)

func (a Action) String() string {
	switch a {
	case ActionPush:
		return "PUSH"
	case ActionPull:
		return "PULL"
	default:
		return "SKIP"
	}
}

type Result struct {
	Action    Action
	Path      string
	OldLocal  int
	OldRemote int
	NewRating int
	RemoteID  string
}

type LocalFile struct {
	RelPath string
	*tag.LocalFile
}

func ScanLocalFiles(musicPath string, log *slog.Logger) ([]*LocalFile, error) {
	var files []*LocalFile

	err := filepath.WalkDir(musicPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			log.Warn("Error accessing path", "path", path, "error", err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".mp3" && ext != ".flac" {
			return nil
		}

		rel, err := filepath.Rel(musicPath, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path for %s: %w", path, err)
		}
		rel = filepath.ToSlash(rel)

		lf, err := tag.ReadLocalFile(path)
		if err != nil {
			log.Warn("Failed to read file metadata", "path", rel, "error", err)
			lf = &tag.LocalFile{}
		}

		files = append(files, &LocalFile{RelPath: rel, LocalFile: lf})

		log.Debug("Scanned local file", "path", rel, "rating", lf.Rating,
			"mbid", lf.MusicBrainzID, "isrc", lf.ISRC)

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk music path: %w", err)
	}

	log.Info("Scanned local files", "count", len(files))
	return files, nil
}

var discPrefixRe = regexp.MustCompile(`^\d+-`)

func Run(
	musicPath string,
	localFiles []*LocalFile,
	remoteSongs []*navidrome.RemoteSong,
	prefer string,
	dryRun bool,
	log *slog.Logger,
) ([]Result, error) {
	matched := matchLocalToRemote(localFiles, remoteSongs, log)

	var results []Result

	pushed := 0
	pulled := 0
	skipped := 0
	conflicts := 0

	for _, m := range matched {
		localRating := m.local.Rating
		remoteRating := m.remote.UserRating

		if localRating == 0 && remoteRating == 0 {
			skipped++
			results = append(results, Result{Action: ActionSkip, Path: m.local.RelPath})
			continue
		}

		if localRating == remoteRating {
			skipped++
			results = append(results, Result{Action: ActionSkip, Path: m.local.RelPath})
			continue
		}

		if localRating > 0 && remoteRating == 0 {
			results = append(results, Result{
				Action: ActionPush, Path: m.local.RelPath,
				OldLocal: localRating, OldRemote: remoteRating, NewRating: localRating,
				RemoteID: m.remote.ID,
			})
			pushed++
			continue
		}

		if remoteRating > 0 && localRating == 0 {
			results = append(results, Result{
				Action: ActionPull, Path: m.local.RelPath,
				OldLocal: localRating, OldRemote: remoteRating, NewRating: remoteRating,
				RemoteID: m.remote.ID,
			})
			pulled++
			continue
		}

		conflicts++
		var chosenRating int
		var action Action
		if prefer == "local" {
			chosenRating = localRating
			action = ActionPush
		} else {
			chosenRating = remoteRating
			action = ActionPull
		}

		results = append(results, Result{
			Action: action, Path: m.local.RelPath,
			OldLocal: localRating, OldRemote: remoteRating, NewRating: chosenRating,
			RemoteID: m.remote.ID,
		})

		if action == ActionPush {
			pushed++
		} else {
			pulled++
		}
	}

	log.Info("Sync summary",
		"pushed", pushed, "pulled", pulled,
		"skipped", skipped, "conflicts_resolved", conflicts,
		"dry_run", dryRun,
	)

	return results, nil
}

type match struct {
	local  *LocalFile
	remote *navidrome.RemoteSong
	method string
}

func matchLocalToRemote(localFiles []*LocalFile, remoteSongs []*navidrome.RemoteSong, log *slog.Logger) []match {
	sorted := make([]*LocalFile, len(localFiles))
	copy(sorted, localFiles)

	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Rating > 0 && sorted[j].Rating == 0 {
			return true
		}
		if sorted[i].Rating == 0 && sorted[j].Rating > 0 {
			return false
		}
		return false
	})

	indexOf := make(map[*LocalFile]int, len(localFiles))
	for i, lf := range localFiles {
		indexOf[lf] = i
	}

	var results []match
	matchedLocal := make(map[int]bool)
	matchedRemote := make(map[string]bool)

	mbidRemote := make(map[string]*navidrome.RemoteSong)
	for _, rs := range remoteSongs {
		if rs.MusicBrainzID != "" {
			mbidRemote[rs.MusicBrainzID] = rs
		}
	}
	for _, lf := range sorted {
		i := indexOf[lf]
		if lf.MusicBrainzID == "" {
			continue
		}
		if rs, ok := mbidRemote[lf.MusicBrainzID]; ok && !matchedRemote[rs.ID] {
			results = append(results, match{local: lf, remote: rs, method: "musicbrainz_id"})
			matchedLocal[i] = true
			matchedRemote[rs.ID] = true
			log.Debug("Matched by MusicBrainz ID", "local", lf.RelPath, "mbid", lf.MusicBrainzID)
		}
	}

	isrcLocal := make(map[string]int)
	for _, lf := range localFiles {
		if lf.ISRC != "" {
			isrcLocal[lf.ISRC] = indexOf[lf]
		}
	}
	for _, rs := range remoteSongs {
		if matchedRemote[rs.ID] {
			continue
		}
		_ = isrcLocal
	}

	for _, lf := range sorted {
		i := indexOf[lf]
		if matchedLocal[i] {
			continue
		}
		songTitle := discPrefixRe.ReplaceAllString(filepath.Base(lf.RelPath), "")
		for _, rs := range remoteSongs {
			if matchedRemote[rs.ID] {
				continue
			}
			remoteTitle := discPrefixRe.ReplaceAllString(filepath.Base(rs.Path), "")
			if lf.Artist != "" && rs.Artist != "" && lf.Album != "" && rs.Album != "" {
				if strings.EqualFold(lf.Artist, rs.Artist) &&
					strings.EqualFold(lf.Album, rs.Album) &&
					songsMatch(songTitle, remoteTitle) {
					results = append(results, match{local: lf, remote: rs, method: "title"})
					matchedLocal[i] = true
					matchedRemote[rs.ID] = true
					log.Debug("Matched by title", "local", lf.RelPath)
					break
				}
			}
		}
	}

	return results
}

func songsMatch(a, b string) bool {
	if strings.EqualFold(a, b) {
		return true
	}
	clean := func(s string) string {
		s = strings.ToLower(s)
		s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, " ")
		s = strings.Join(strings.Fields(s), " ")
		return s
	}
	return clean(a) == clean(b)
}

func ApplyResults(
	musicPath string,
	results []Result,
	client *navidrome.Client,
	dryRun bool,
	log *slog.Logger,
) error {
	for _, r := range results {
		switch r.Action {
		case ActionSkip:
			continue

		case ActionPush:
			if dryRun {
				log.Info("[DRY-RUN] Would push rating to Navidrome",
					"path", r.Path, "rating", r.NewRating)
			} else {
				if err := client.SetRating(r.RemoteID, r.NewRating); err != nil {
					log.Error("Failed to push rating",
						"path", r.Path, "rating", r.NewRating, "error", err)
					continue
				}
				log.Info("Pushed rating to Navidrome",
					"path", r.Path, "rating", r.NewRating)
			}

		case ActionPull:
			fullPath := filepath.Join(musicPath, r.Path)
			if dryRun {
				log.Info("[DRY-RUN] Would write rating to local file",
					"path", r.Path, "rating", r.NewRating)
			} else {
				if err := tag.WriteRating(fullPath, r.NewRating); err != nil {
					log.Error("Failed to write rating",
						"path", r.Path, "rating", r.NewRating, "error", err)
					continue
				}
				log.Info("Wrote rating to local file",
					"path", r.Path, "rating", r.NewRating)
			}
		}
	}

	return nil
}
