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
	"net/url"
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
	Starred       string
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
	Starred       string `json:"starred,omitempty"`
	MusicBrainzID string `json:"musicBrainzId,omitempty"`
	Artist        string `json:"artist,omitempty"`
	Album         string `json:"album,omitempty"`
}

type SearchResult3 struct {
	Song []Song `json:"song,omitempty"`
}

type RemotePlaylist struct {
	ID        string
	Name      string
	Comment   string
	Owner     string
	Public    bool
	SongCount int
	Duration  int
	Created   string
	Changed   string
	Readonly  bool
	Entry     []Song
}

type playlistDTO struct {
	ID         string `json:"id,omitempty"`
	Name       string `json:"name,omitempty"`
	Comment    string `json:"comment,omitempty"`
	Owner      string `json:"owner,omitempty"`
	Public     bool   `json:"public,omitempty"`
	SongCount  int    `json:"songCount,omitempty"`
	Duration   int    `json:"duration,omitempty"`
	Created    string `json:"created,omitempty"`
	Changed    string `json:"changed,omitempty"`
	Readonly   bool   `json:"readonly,omitempty"`
	ValidUntil string `json:"validUntil,omitempty"`
	Entry      []Song `json:"entry,omitempty"`
}

type playlistsDTO struct {
	Playlist []playlistDTO `json:"playlist,omitempty"`
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

func (c *Client) Star(ctx context.Context, id string) error {
	_, err := c.do(ctx, "star", map[string]string{"id": id})
	if err != nil {
		return fmt.Errorf("starring remote song %q: %w", id, err)
	}
	return nil
}

func (c *Client) Unstar(ctx context.Context, id string) error {
	_, err := c.do(ctx, "unstar", map[string]string{"id": id})
	if err != nil {
		return fmt.Errorf("unstarring remote song %q: %w", id, err)
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

func (c *Client) Playlists(ctx context.Context) ([]RemotePlaylist, error) {
	body, err := c.do(ctx, "getPlaylists", nil)
	if err != nil {
		return nil, fmt.Errorf("listing playlists: %w", err)
	}
	if body.Playlists == nil {
		return nil, nil
	}
	playlists := make([]RemotePlaylist, 0, len(body.Playlists.Playlist))
	for _, p := range body.Playlists.Playlist {
		playlists = append(playlists, remotePlaylist(p))
	}
	return playlists, nil
}

func (c *Client) Playlist(ctx context.Context, id string) (*RemotePlaylist, error) {
	body, err := c.do(ctx, "getPlaylist", map[string]string{"id": id})
	if err != nil {
		return nil, fmt.Errorf("fetching playlist %q: %w", id, err)
	}
	if body.Playlist == nil {
		return nil, nil
	}
	p := remotePlaylist(*body.Playlist)
	return &p, nil
}

func (c *Client) CreatePlaylist(ctx context.Context, name string, songIDs []string) (*RemotePlaylist, error) {
	params := url.Values{"name": []string{name}}
	for _, id := range songIDs {
		params.Add("songId", id)
	}
	body, err := c.doValues(ctx, "createPlaylist", params)
	if err != nil {
		return nil, fmt.Errorf("creating playlist %q: %w", name, err)
	}
	if body.Playlist == nil {
		return nil, nil
	}
	p := remotePlaylist(*body.Playlist)
	return &p, nil
}

func (c *Client) ReplacePlaylist(ctx context.Context, playlistID string, songIDs []string) (*RemotePlaylist, error) {
	params := url.Values{"playlistId": []string{playlistID}}
	for _, id := range songIDs {
		params.Add("songId", id)
	}
	body, err := c.doValues(ctx, "createPlaylist", params)
	if err != nil {
		return nil, fmt.Errorf("replacing playlist %q: %w", playlistID, err)
	}
	if body.Playlist == nil {
		return nil, nil
	}
	p := remotePlaylist(*body.Playlist)
	return &p, nil
}

func (c *Client) UpdatePlaylist(ctx context.Context, playlistID string, public *bool) error {
	params := map[string]string{"playlistId": playlistID}
	if public != nil {
		params["public"] = strconv.FormatBool(*public)
	}
	_, err := c.do(ctx, "updatePlaylist", params)
	if err != nil {
		return fmt.Errorf("updating playlist %q: %w", playlistID, err)
	}
	return nil
}

func (c *Client) DeletePlaylist(ctx context.Context, id string) error {
	_, err := c.do(ctx, "deletePlaylist", map[string]string{"id": id})
	if err != nil {
		return fmt.Errorf("deleting playlist %q: %w", id, err)
	}
	return nil
}

func remotePlaylist(p playlistDTO) RemotePlaylist {
	return RemotePlaylist{
		ID:        p.ID,
		Name:      p.Name,
		Comment:   p.Comment,
		Owner:     p.Owner,
		Public:    p.Public,
		SongCount: p.SongCount,
		Duration:  p.Duration,
		Created:   p.Created,
		Changed:   p.Changed,
		Readonly:  p.Readonly,
		Entry:     p.Entry,
	}
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

		userRating, playCount, played, starred := enrichSong(details)
		results = append(results, &RemoteSong{
			ID:            song.ID,
			Path:          song.Path,
			UserRating:    userRating,
			PlayCount:     playCount,
			Played:        played,
			Starred:       starred,
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

// enrichSong extracts per-user metadata from a song detail response.
// details may be nil when the Subsonic server returns no detail for a track.
func enrichSong(details *Song) (userRating int, playCount int64, played, starred string) {
	if details == nil {
		return
	}
	return details.UserRating, details.PlayCount, details.Played, details.Starred
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
	Playlists    *playlistsDTO  `json:"playlists,omitempty"`
	Playlist     *playlistDTO   `json:"playlist,omitempty"`
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
	values := url.Values{}
	for key, value := range params {
		values.Set(key, value)
	}
	for key, value := range queryParams {
		values.Set(key, value)
	}
	return c.doValues(ctx, endpoint, values)
}

func (c *Client) doValues(ctx context.Context, endpoint string, queryParams url.Values) (*responseBody, error) {
	params, err := c.authParams()
	if err != nil {
		return nil, err
	}
	values := url.Values{}
	for key, value := range params {
		values.Set(key, value)
	}
	for key, items := range queryParams {
		for _, value := range items {
			values.Add(key, value)
		}
	}

	var envelope responseEnvelope
	resp, err := c.http.R().
		SetContext(ctx).
		SetQueryParamsFromValues(values).
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
