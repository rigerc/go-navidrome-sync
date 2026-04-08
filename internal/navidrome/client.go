package navidrome

import (
	"context"
	"crypto/md5" //nolint:gosec // required by Subsonic API protocol
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	goenvoynavidrome "github.com/lusoris/goenvoy/mediaserver/navidrome"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/config"
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
	*goenvoynavidrome.Client
	baseURL  string
	username string
	password string
	http     *http.Client
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
	c := goenvoynavidrome.New(
		cfg.Navidrome.BaseURL,
		cfg.Navidrome.User,
		cfg.Navidrome.Password,
		goenvoynavidrome.WithHTTPClient(httpClient),
	)

	if err := c.Ping(ctx); err != nil {
		var subsonicErr *goenvoynavidrome.SubsonicError
		if errors.As(err, &subsonicErr) && subsonicErr.Code == subsonicAuthErrorCode {
			return nil, fmt.Errorf("authentication failed for user %q (check user/password)", cfg.Navidrome.User)
		}
		return nil, fmt.Errorf("connection failed: %w (check baseurl %q)", err, cfg.Navidrome.BaseURL)
	}

	logger.Info("Connected to Navidrome", "url", cfg.Navidrome.BaseURL, "user", cfg.Navidrome.User)

	return &Client{
		Client:   c,
		baseURL:  strings.TrimRight(cfg.Navidrome.BaseURL, "/"),
		username: cfg.Navidrome.User,
		password: cfg.Navidrome.Password,
		http:     httpClient,
		log:      logger,
	}, nil
}

func (c *Client) SearchSongsByTitle(ctx context.Context, title string, limit int) ([]*RemoteSong, error) {
	if limit <= 0 {
		return nil, nil
	}

	startedAt := time.Now()
	c.log.Debug("Navidrome search request started", "query", title, "limit", limit)
	searchResult, err := c.search3(ctx, title, limit)
	if err != nil {
		c.log.Warn("Navidrome search request failed",
			"query", title,
			"limit", limit,
			"duration", time.Since(startedAt),
			"error", err,
		)
		return nil, fmt.Errorf("searching tracks for %q: %w", title, err)
	}
	c.log.Debug("Navidrome search request completed",
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
		results = append(results, &RemoteSong{
			ID:            song.ID,
			Path:          song.Path,
			UserRating:    song.UserRating,
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

func (c *Client) SetRating(ctx context.Context, id string, rating int) error {
	if err := c.setRating(ctx, id, rating); err != nil {
		return fmt.Errorf("setting rating for remote song %q: %w", id, err)
	}
	return nil
}

type responseEnvelope struct {
	Response responseBody `json:"subsonic-response"`
}

type responseBody struct {
	Status string         `json:"status"`
	Error  *subsonicError `json:"error,omitempty"`

	SearchResult *searchResult3 `json:"searchResult3,omitempty"`
}

type subsonicError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *subsonicError) Error() string {
	return e.Message
}

type searchResult3 struct {
	Song []remoteSong `json:"song,omitempty"`
}

type remoteSong struct {
	ID            string `json:"id"`
	IsDir         bool   `json:"isDir,omitempty"`
	Path          string `json:"path,omitempty"`
	UserRating    int    `json:"userRating,omitempty"`
	MusicBrainzID string `json:"musicBrainzId,omitempty"`
	Artist        string `json:"artist,omitempty"`
	Album         string `json:"album,omitempty"`
}

func (c *Client) search3(ctx context.Context, query string, songCount int) (*searchResult3, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("artistCount", "0")
	params.Set("albumCount", "0")
	params.Set("songCount", strconv.Itoa(songCount))

	body, err := c.get(ctx, "search3", params)
	if err != nil {
		return nil, err
	}
	return body.SearchResult, nil
}

func searchResultSongs(result *searchResult3) []remoteSong {
	if result == nil {
		return nil
	}
	return result.Song
}

func (c *Client) setRating(ctx context.Context, id string, rating int) error {
	params := url.Values{}
	params.Set("id", id)
	params.Set("rating", strconv.Itoa(rating))

	_, err := c.get(ctx, "setRating", params)
	return err
}

func (c *Client) get(ctx context.Context, endpoint string, extra url.Values) (*responseBody, error) {
	params, err := c.authParams()
	if err != nil {
		return nil, err
	}

	for key, values := range extra {
		for _, value := range values {
			params.Add(key, value)
		}
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.baseURL+"/rest/"+endpoint+"?"+encodeQueryValues(params),
		http.NoBody,
	)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode > http.StatusMultipleChoices-1 {
		return nil, &goenvoynavidrome.APIError{StatusCode: resp.StatusCode, Status: resp.Status, Body: string(body)}
	}

	var envelope responseEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	if envelope.Response.Status != "ok" {
		if envelope.Response.Error != nil {
			return nil, envelope.Response.Error
		}
		return nil, fmt.Errorf("navidrome: unexpected status %q", envelope.Response.Status)
	}

	return &envelope.Response, nil
}

func (c *Client) authParams() (url.Values, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}

	s := hex.EncodeToString(salt)
	sum := md5.Sum([]byte(c.password + s))
	token := hex.EncodeToString(sum[:])

	params := url.Values{}
	params.Set("u", c.username)
	params.Set("t", token)
	params.Set("s", s)
	params.Set("v", apiVersion)
	params.Set("c", clientName)
	params.Set("f", "json")
	return params, nil
}

func encodeQueryValues(values url.Values) string {
	if len(values) == 0 {
		return ""
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		encodedKey := strictPercentEncode(key)
		for _, value := range values[key] {
			parts = append(parts, encodedKey+"="+strictPercentEncode(value))
		}
	}
	return strings.Join(parts, "&")
}

func strictPercentEncode(s string) string {
	if s == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(s) * 3)
	for _, ch := range []byte(s) {
		switch {
		case ch >= 'A' && ch <= 'Z':
			b.WriteByte(ch)
		case ch >= 'a' && ch <= 'z':
			b.WriteByte(ch)
		case ch >= '0' && ch <= '9':
			b.WriteByte(ch)
		default:
			b.WriteByte('%')
			b.WriteByte(upperHex[ch>>4])
			b.WriteByte(upperHex[ch&0x0F])
		}
	}
	return b.String()
}

const upperHex = "0123456789ABCDEF"
