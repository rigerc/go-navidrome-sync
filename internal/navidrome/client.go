package navidrome

import (
	"context"
	"crypto/md5" //nolint:gosec // required by Subsonic API protocol
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/go-resty/resty/v2"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/config"
)

type RemoteSong struct {
	ID            string
	Path          string
	UserRating    int
	PlayCount     int64
	Played        string
	MusicBrainzID string
	Artist        string
	Album         string
}

type Song struct {
	ID            string `json:"id,omitempty"`
	Path          string `json:"path,omitempty"`
	IsDir         bool   `json:"isDir,omitempty"`
	UserRating    int    `json:"userRating,omitempty"`
	PlayCount     int64  `json:"playCount,omitempty"`
	Played        string `json:"played,omitempty"`
	MusicBrainzID string `json:"musicBrainzId,omitempty"`
	Artist        string `json:"artist,omitempty"`
	Album         string `json:"album,omitempty"`
}

type SearchResult3 struct {
	Song []Song `json:"song,omitempty"`
}

type Client struct {
	baseURL  string
	username string
	password string
	http     *resty.Client
	native   *nativeSearchClient
	log      *log.Logger
}

const (
	requestTimeout        = 15 * time.Second
	maxIdleConns          = 10
	maxIdlePerHost        = 4
	apiVersion            = "1.16.1"
	clientName            = "go-navidrome-ratings-sync"
	saltLength            = 12
	subsonicAuthErrorCode = 40
)

func Connect(ctx context.Context, cfg *config.Config, logger *log.Logger) (*Client, error) {
	httpClient := &http.Client{
		Timeout: requestTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: cfg.Navidrome.TLSSkipVerify,
			},
			MaxIdleConns:        maxIdleConns,
			MaxIdleConnsPerHost: maxIdlePerHost,
		},
	}

	client := resty.NewWithClient(httpClient).
		SetBaseURL(strings.TrimRight(cfg.Navidrome.BaseURL, "/")).
		SetHeader("Accept", "application/json")

	c := &Client{
		baseURL:  strings.TrimRight(cfg.Navidrome.BaseURL, "/"),
		username: cfg.Navidrome.User,
		password: cfg.Navidrome.Password,
		http:     client,
		native: newNativeSearchClient(
			strings.TrimRight(cfg.Navidrome.BaseURL, "/"),
			cfg.Navidrome.User,
			cfg.Navidrome.Password,
			httpClient,
			logger,
		),
		log: logger,
	}

	if err := c.Ping(ctx); err != nil {
		var subsonicErr *subsonicError
		if ok := errors.As(err, &subsonicErr); ok && subsonicErr.Code == subsonicAuthErrorCode {
			return nil, fmt.Errorf("authentication failed for user %q (check user/password)", cfg.Navidrome.User)
		}
		return nil, fmt.Errorf("connection failed: %w (check baseurl %q)", err, cfg.Navidrome.BaseURL)
	}

	logger.Debug("Connected to Navidrome", "url", cfg.Navidrome.BaseURL, "user", cfg.Navidrome.User)
	return c, nil
}

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.do(ctx, "ping", nil)
	return err
}

func (c *Client) Search3(ctx context.Context, query string, songCount int) (*SearchResult3, error) {
	params := map[string]string{
		"query":       query,
		"artistCount": "0",
		"albumCount":  "0",
		"songCount":   strconv.Itoa(songCount),
	}

	body, err := c.do(ctx, "search3", params)
	if err != nil {
		return nil, fmt.Errorf("search3 %q: %w", query, err)
	}
	return body.SearchResult, nil
}

func (c *Client) GetSong(ctx context.Context, id string) (*Song, error) {
	body, err := c.do(ctx, "getSong", map[string]string{"id": id})
	if err != nil {
		return nil, fmt.Errorf("getSong %q: %w", id, err)
	}
	return body.Song, nil
}

func (c *Client) GetRating(ctx context.Context, id string) (int, error) {
	song, err := c.GetSong(ctx, id)
	if err != nil {
		return 0, err
	}
	if song == nil {
		return 0, nil
	}
	return song.UserRating, nil
}

func (c *Client) SetRating(ctx context.Context, id string, rating int) error {
	_, err := c.do(ctx, "setRating", map[string]string{
		"id":     id,
		"rating": strconv.Itoa(rating),
	})
	if err != nil {
		return fmt.Errorf("setting rating for remote song %q: %w", id, err)
	}
	return nil
}

func (c *Client) Scrobble(ctx context.Context, id string, n int, playedAt time.Time) error {
	for i := range n {
		offset := time.Duration(n-1-i) * time.Minute
		t := playedAt.Add(-offset)
		_, err := c.do(ctx, "scrobble", map[string]string{
			"id":         id,
			"time":       strconv.FormatInt(t.UnixMilli(), 10),
			"submission": "true",
		})
		if err != nil {
			return fmt.Errorf("scrobbling %q (play %d/%d): %w", id, i+1, n, err)
		}
	}
	return nil
}

func (c *Client) SearchSongsByTitle(ctx context.Context, title string, limit int) ([]*RemoteSong, error) {
	if limit <= 0 {
		return nil, nil
	}

	startedAt := time.Now()
	c.log.Debug("Starting remote song search via Subsonic API",
		"source", "subsonic",
		"query", title,
		"limit", limit,
	)

	searchResult, err := c.Search3(ctx, title, limit)
	if err != nil {
		c.log.Warn("Remote song search via Subsonic API failed",
			"source", "subsonic",
			"query", title,
			"limit", limit,
			"duration", time.Since(startedAt),
			"error", err,
		)
		return nil, fmt.Errorf("searching tracks for %q: %w", title, err)
	}

	c.log.Debug("Completed remote song search via Subsonic API",
		"source", "subsonic",
		"query", title,
		"limit", limit,
		"duration", time.Since(startedAt),
		"song_count", len(searchResultSongs(searchResult)),
	)

	songs := searchResultSongs(searchResult)
	results := make([]*RemoteSong, 0, min(limit, len(songs)))
	for _, song := range songs {
		if song.IsDir {
			continue
		}

		details, err := c.GetSong(ctx, song.ID)
		if err != nil {
			return nil, fmt.Errorf("fetching search result track details for %q: %w", song.ID, err)
		}

		userRating := 0
		if details != nil {
			userRating = details.UserRating
		}
		playCount := int64(0)
		played := ""
		if details != nil {
			playCount = details.PlayCount
			played = details.Played
		}
		results = append(results, &RemoteSong{
			ID:            song.ID,
			Path:          song.Path,
			UserRating:    userRating,
			PlayCount:     playCount,
			Played:        played,
			MusicBrainzID: song.MusicBrainzID,
			Artist:        song.Artist,
			Album:         song.Album,
		})
		if len(results) == limit {
			break
		}
	}

	return results, nil
}

func (c *Client) SearchSongsByTitleFallback(ctx context.Context, title string, limit int) ([]*RemoteSong, error) {
	if c.native == nil {
		c.log.Debug("Skipping native Navidrome fallback search because no native client is configured",
			"source", "native",
			"title", title,
			"limit", limit,
		)
		return nil, nil
	}
	return c.native.SearchSongsByTitle(ctx, title, limit)
}

func searchResultSongs(result *SearchResult3) []Song {
	if result == nil {
		return nil
	}
	return result.Song
}

type responseEnvelope struct {
	Response responseBody `json:"subsonic-response"`
}

type responseBody struct {
	Status string         `json:"status"`
	Error  *subsonicError `json:"error,omitempty"`

	Song         *Song          `json:"song,omitempty"`
	SearchResult *SearchResult3 `json:"searchResult3,omitempty"`
}

type subsonicError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *subsonicError) Error() string {
	return e.Message
}

type apiError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("unexpected response status %s", e.Status)
}

func (c *Client) do(ctx context.Context, endpoint string, queryParams map[string]string) (*responseBody, error) {
	params, err := c.authParams()
	if err != nil {
		return nil, err
	}
	for key, value := range queryParams {
		params[key] = value
	}

	var envelope responseEnvelope
	resp, err := c.http.R().
		SetContext(ctx).
		SetQueryParams(params).
		SetResult(&envelope).
		Get("/rest/" + endpoint)
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		return nil, &apiError{
			StatusCode: resp.StatusCode(),
			Status:     resp.Status(),
			Body:       string(resp.Body()),
		}
	}
	if envelope.Response.Status != "ok" {
		if envelope.Response.Error != nil {
			return nil, envelope.Response.Error
		}
		return nil, fmt.Errorf("navidrome: unexpected status %q", envelope.Response.Status)
	}

	return &envelope.Response, nil
}

func (c *Client) authParams() (map[string]string, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}

	s := hex.EncodeToString(salt)
	sum := md5.Sum([]byte(c.password + s))
	token := hex.EncodeToString(sum[:])

	return map[string]string{
		"u": c.username,
		"t": token,
		"s": s,
		"v": apiVersion,
		"c": clientName,
		"f": "json",
	}, nil
}
