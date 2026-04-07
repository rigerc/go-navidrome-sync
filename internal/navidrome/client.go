package navidrome

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/rigerc/go-navidrome-ratings-sync/internal/config"
	subsonic "github.com/supersonic-app/go-subsonic/subsonic"
)

type RemoteSong struct {
	ID            string
	Path          string
	UserRating    int
	MusicBrainzID string
	Artist        string
	Album         string
}

type Client struct {
	*subsonic.Client
	log *slog.Logger
}

func Connect(cfg *config.Config, log *slog.Logger) (*Client, error) {
	c := &subsonic.Client{
		Client: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: cfg.Navidrome.TLSSkipVerify,
				},
			},
		},
		BaseUrl:    strings.TrimRight(cfg.Navidrome.BaseURL, "/"),
		User:       cfg.Navidrome.User,
		ClientName: "go-navidrome-ratings-sync",
		UseJSON:    true,
	}

	if err := c.Authenticate(cfg.Navidrome.Password); err != nil {
		if errors.Is(err, subsonic.ErrAuthenticationFailure) {
			return nil, fmt.Errorf("authentication failed for user %q (check user/password)", cfg.Navidrome.User)
		}
		return nil, fmt.Errorf("connection failed: %w (check baseurl %q)", err, cfg.Navidrome.BaseURL)
	}

	log.Info("Connected to Navidrome", "url", c.BaseUrl, "user", cfg.Navidrome.User)

	return &Client{Client: c, log: log}, nil
}

const searchPageSize = 500

func (c *Client) FetchSongsForArtists(artistNames map[string]bool) ([]*RemoteSong, error) {
	artists := make([]string, 0, len(artistNames))
	for name := range artistNames {
		artists = append(artists, name)
	}
	sort.Strings(artists)

	var results []*RemoteSong
	for _, artistName := range artists {
		offset := 0
		for {
			searchResult, err := c.Search3(artistName, map[string]string{
				"artistCount": "0",
				"albumCount":  "0",
				"songCount":   fmt.Sprintf("%d", searchPageSize),
				"songOffset":  fmt.Sprintf("%d", offset),
			})
			if err != nil {
				c.log.Warn("Failed to search artist", "artist", artistName, "error", err)
				break
			}

			for _, song := range searchResult.Song {
				if song.IsDir {
					continue
				}
				results = append(results, &RemoteSong{
					ID:            song.ID,
					Path:          song.Path,
					UserRating:    song.UserRating,
					MusicBrainzID: song.MusicBrainzID,
					Artist:        song.Artist,
					Album:         song.Album,
				})
			}

			if len(searchResult.Song) < searchPageSize {
				break
			}
			offset += searchPageSize
		}
	}

	c.log.Debug("Fetched remote songs", "count", len(results))
	return results, nil
}
