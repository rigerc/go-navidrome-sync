package navidrome

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/charmbracelet/log"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/config"
)

func TestConnect_AuthenticationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"subsonic-response": map[string]any{
				"status":  "failed",
				"version": "1.16.1",
				"error": map[string]any{
					"code":    40,
					"message": "Wrong username or password.",
				},
			},
		})
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = server.URL
	cfg.Navidrome.User = "alice"
	cfg.Navidrome.Password = "bad-password"

	_, err := Connect(context.Background(), cfg, log.New(io.Discard))
	if err == nil || err.Error() != `authentication failed for user "alice" (check user/password)` {
		t.Fatalf("Connect() error = %v", err)
	}
}

func TestSearchSongsByTitle_DecodesUserRating(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/rest/search3" {
			t.Fatalf("path = %q, want %q", got, "/rest/search3")
		}
		if got := r.URL.Query().Get("query"); got != "beatles" {
			t.Fatalf("query = %q, want %q", got, "beatles")
		}
		if got := r.URL.Query().Get("songCount"); got != "2" {
			t.Fatalf("songCount = %q, want %q", got, "2")
		}

		writeJSON(t, w, map[string]any{
			"subsonic-response": map[string]any{
				"status":  "ok",
				"version": "1.16.1",
				"searchResult3": map[string]any{
					"song": []map[string]any{
						{
							"id":            "song-1",
							"path":          "artist/album/track.flac",
							"userRating":    4,
							"musicBrainzId": "mbid-1",
							"artist":        "Artist",
							"album":         "Album",
						},
						{
							"id":    "folder-1",
							"isDir": true,
						},
					},
				},
			},
		})
	})

	results, err := client.SearchSongsByTitle(context.Background(), "beatles", 2)
	if err != nil {
		t.Fatalf("SearchSongsByTitle() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].UserRating != 4 {
		t.Fatalf("results[0].UserRating = %d, want 4", results[0].UserRating)
	}
	if results[0].MusicBrainzID != "mbid-1" {
		t.Fatalf("results[0].MusicBrainzID = %q, want %q", results[0].MusicBrainzID, "mbid-1")
	}
}

func TestSearchSongsByTitle_StrictlyPercentEncodesNonAlphanumericQueryBytes(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		want := "albumCount=0&artistCount=0&c=go%2Dnavidrome%2Dratings%2Dsync&f=json&query=%C3%A4%C3%B6%C3%BC%C2%B1%C2%B0%2B%26&"
		if got := r.URL.RawQuery; !strings.Contains(got, want) {
			t.Fatalf("RawQuery = %q, want substring %q", got, want)
		}

		writeJSON(t, w, map[string]any{
			"subsonic-response": map[string]any{
				"status":  "ok",
				"version": "1.16.1",
				"searchResult3": map[string]any{
					"song": []map[string]any{},
				},
			},
		})
	})

	if _, err := client.SearchSongsByTitle(context.Background(), "äöü±°+&", 1); err != nil {
		t.Fatalf("SearchSongsByTitle() error = %v", err)
	}
}

func TestSetRating_SendsExpectedRequest(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/rest/setRating" {
			t.Fatalf("path = %q, want %q", got, "/rest/setRating")
		}
		if got := r.URL.Query().Get("id"); got != "song-1" {
			t.Fatalf("id = %q, want %q", got, "song-1")
		}
		if got := r.URL.Query().Get("rating"); got != "5" {
			t.Fatalf("rating = %q, want %q", got, "5")
		}
		if got := r.URL.Query().Get("u"); got != "admin" {
			t.Fatalf("u = %q, want %q", got, "admin")
		}

		writeJSON(t, w, map[string]any{
			"subsonic-response": map[string]any{
				"status":  "ok",
				"version": "1.16.1",
			},
		})
	})

	if err := client.SetRating(context.Background(), "song-1", 5); err != nil {
		t.Fatalf("SetRating() error = %v", err)
	}
}

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	httpClient := server.Client()
	return &Client{
		Client:   nil,
		baseURL:  server.URL,
		username: "admin",
		password: "password",
		http:     httpClient,
		log:      log.New(io.Discard),
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
}
