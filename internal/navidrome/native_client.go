package navidrome

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/go-resty/resty/v2"
)

const nativeAuthorizationHeader = "X-ND-Authorization"

type nativeSearchClient struct {
	username string
	password string
	http     *resty.Client
	log      *log.Logger

	mu    sync.Mutex
	token string
}

type nativeAuthResponse struct {
	Token string `json:"token"`
}

type nativeSong struct {
	ID             string `json:"id"`
	Path           string `json:"path"`
	Rating         int    `json:"rating"`
	PlayCount      int64  `json:"playCount"`
	PlayDate       string `json:"playDate"`
	MbzRecordingID string `json:"mbzRecordingID"`
	Artist         string `json:"artist"`
	Album          string `json:"album"`
}

func newNativeSearchClient(baseURL, username, password string, httpClient *http.Client, logger *log.Logger) *nativeSearchClient {
	return &nativeSearchClient{
		username: username,
		password: password,
		http: resty.NewWithClient(httpClient).
			SetBaseURL(baseURL).
			SetHeader("Accept", "application/json"),
		log: logger,
	}
}

func (c *nativeSearchClient) SearchSongsByTitle(ctx context.Context, title string, limit int) ([]*RemoteSong, error) {
	if limit <= 0 {
		return nil, nil
	}
	if strings.TrimSpace(title) == "" {
		return nil, nil
	}

	c.log.Debug("Starting remote song search via Navidrome native API",
		"source", "native",
		"title", title,
		"limit", limit,
	)
	startedAt := time.Now()

	if err := c.ensureToken(ctx); err != nil {
		return nil, err
	}

	songs, err := c.searchSongs(ctx, title, limit)
	if err != nil {
		if isUnauthorized(err) {
			c.log.Debug("Native Navidrome token was rejected, retrying login",
				"source", "native",
				"title", title,
				"elapsed", time.Since(startedAt),
			)
			c.clearToken()
			if authErr := c.ensureToken(ctx); authErr != nil {
				return nil, authErr
			}
			songs, err = c.searchSongs(ctx, title, limit)
		}
		if err != nil {
			return nil, err
		}
	}

	results := make([]*RemoteSong, 0, len(songs))
	for _, song := range songs {
		results = append(results, &RemoteSong{
			ID:            song.ID,
			Path:          song.Path,
			UserRating:    song.Rating,
			PlayCount:     song.PlayCount,
			Played:        song.PlayDate,
			MusicBrainzID: song.MbzRecordingID,
			Artist:        song.Artist,
			Album:         song.Album,
		})
	}
	c.log.Debug("Completed remote song search via Navidrome native API",
		"source", "native",
		"title", title,
		"limit", limit,
		"song_count", len(results),
		"duration", time.Since(startedAt),
	)
	return results, nil
}

func (c *nativeSearchClient) ensureToken(ctx context.Context) error {
	c.mu.Lock()
	token := c.token
	c.mu.Unlock()
	if token != "" {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" {
		return nil
	}

	c.log.Debug("Logging in to Navidrome native API",
		"source", "native",
		"user", c.username,
	)
	startedAt := time.Now()

	var auth nativeAuthResponse
	resp, err := c.http.R().
		SetContext(ctx).
		SetBody(map[string]string{
			"username": c.username,
			"password": c.password,
		}).
		SetResult(&auth).
		Post("/auth/login")
	if err != nil {
		c.log.Warn("Navidrome native API login failed",
			"source", "native",
			"user", c.username,
			"error", err,
			"duration", time.Since(startedAt),
		)
		return fmt.Errorf("native navidrome login: %w", err)
	}
	if resp.IsError() {
		c.log.Warn("Navidrome native API login returned an error response",
			"source", "native",
			"user", c.username,
			"status", resp.Status(),
			"duration", time.Since(startedAt),
		)
		return &apiError{
			StatusCode: resp.StatusCode(),
			Status:     resp.Status(),
			Body:       string(resp.Body()),
		}
	}
	if auth.Token == "" {
		c.log.Warn("Navidrome native API login returned an empty token",
			"source", "native",
			"user", c.username,
			"duration", time.Since(startedAt),
		)
		return fmt.Errorf("native navidrome login returned empty token")
	}

	c.token = auth.Token
	c.log.Debug("Authenticated with Navidrome native API",
		"source", "native",
		"user", c.username,
		"duration", time.Since(startedAt),
	)
	return nil
}

func (c *nativeSearchClient) searchSongs(ctx context.Context, title string, limit int) ([]nativeSong, error) {
	c.mu.Lock()
	token := c.token
	c.mu.Unlock()

	startedAt := time.Now()
	var songs []nativeSong
	resp, err := c.http.R().
		SetContext(ctx).
		SetHeader(nativeAuthorizationHeader, "Bearer "+token).
		SetQueryParams(map[string]string{
			"title":  title,
			"_start": "0",
			"_end":   strconv.Itoa(limit),
			"_sort":  "title",
			"_order": "ASC",
		}).
		SetResult(&songs).
		Get("/song")
	if err != nil {
		c.log.Warn("Navidrome native API song search failed",
			"source", "native",
			"title", title,
			"error", err,
			"duration", time.Since(startedAt),
		)
		return nil, fmt.Errorf("native navidrome search %q: %w", title, err)
	}
	if resp.IsError() {
		c.log.Warn("Navidrome native API song search returned an error response",
			"source", "native",
			"title", title,
			"status", resp.Status(),
			"duration", time.Since(startedAt),
		)
		return nil, &apiError{
			StatusCode: resp.StatusCode(),
			Status:     resp.Status(),
			Body:       string(resp.Body()),
		}
	}
	c.log.Debug("Navidrome native API song search response received",
		"source", "native",
		"title", title,
		"song_count", len(songs),
		"duration", time.Since(startedAt),
	)
	return songs, nil
}

func (c *nativeSearchClient) clearToken() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
}

func isUnauthorized(err error) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == http.StatusUnauthorized
}
