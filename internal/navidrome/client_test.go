package navidrome

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/log"
	"github.com/go-resty/resty/v2"
	"github.com/rigerc/go-navidrome-sync/internal/config"
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

func TestPing_SendsExpectedAuthAndClientParams(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/rest/ping" {
			t.Fatalf("path = %q, want %q", got, "/rest/ping")
		}
		if got := r.URL.Query().Get("u"); got != "admin" {
			t.Fatalf("u = %q, want %q", got, "admin")
		}
		if got := r.URL.Query().Get("v"); got != apiVersion {
			t.Fatalf("v = %q, want %q", got, apiVersion)
		}
		if got := r.URL.Query().Get("c"); got != clientName {
			t.Fatalf("c = %q, want %q", got, clientName)
		}
		if got := r.URL.Query().Get("f"); got != "json" {
			t.Fatalf("f = %q, want %q", got, "json")
		}
		if got := r.URL.Query().Get("t"); got == "" {
			t.Fatal("t was empty")
		}
		if got := r.URL.Query().Get("s"); got == "" {
			t.Fatal("s was empty")
		}

		writeJSON(t, w, map[string]any{
			"subsonic-response": map[string]any{
				"status":  "ok",
				"version": "1.16.1",
			},
		})
	})

	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
}

func TestSearch3_SendsExpectedRequest(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/rest/search3" {
			t.Fatalf("path = %q, want %q", got, "/rest/search3")
		}
		if got := r.URL.Query().Get("query"); got != "äöü±°+&" {
			t.Fatalf("query = %q, want %q", got, "äöü±°+&")
		}
		if got := r.URL.Query().Get("artistCount"); got != "0" {
			t.Fatalf("artistCount = %q, want %q", got, "0")
		}
		if got := r.URL.Query().Get("albumCount"); got != "0" {
			t.Fatalf("albumCount = %q, want %q", got, "0")
		}
		if got := r.URL.Query().Get("songCount"); got != "2" {
			t.Fatalf("songCount = %q, want %q", got, "2")
		}
		if got := r.URL.Query().Get("c"); got != clientName {
			t.Fatalf("c = %q, want %q", got, clientName)
		}

		writeJSON(t, w, map[string]any{
			"subsonic-response": map[string]any{
				"status":  "ok",
				"version": "1.16.1",
				"searchResult3": map[string]any{
					"song": []map[string]any{
						{"id": "song-1"},
					},
				},
			},
		})
	})

	result, err := client.Search3(context.Background(), "äöü±°+&", 2)
	if err != nil {
		t.Fatalf("Search3() error = %v", err)
	}
	if got := len(result.Song); got != 1 {
		t.Fatalf("len(result.Song) = %d, want 1", got)
	}
}

func TestGetSongAndGetRating_DecodeUserRating(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/rest/getSong" {
			t.Fatalf("path = %q, want %q", got, "/rest/getSong")
		}
		if got := r.URL.Query().Get("id"); got != "song-1" {
			t.Fatalf("id = %q, want %q", got, "song-1")
		}

		writeJSON(t, w, map[string]any{
			"subsonic-response": map[string]any{
				"status":  "ok",
				"version": "1.16.1",
				"song": map[string]any{
					"id":         "song-1",
					"userRating": 4,
				},
			},
		})
	})

	song, err := client.GetSong(context.Background(), "song-1")
	if err != nil {
		t.Fatalf("GetSong() error = %v", err)
	}
	if song == nil || song.UserRating != 4 {
		t.Fatalf("song.UserRating = %v, want 4", song)
	}

	rating, err := client.GetRating(context.Background(), "song-1")
	if err != nil {
		t.Fatalf("GetRating() error = %v", err)
	}
	if rating != 4 {
		t.Fatalf("rating = %d, want 4", rating)
	}
}

func TestSearchSongsByTitle_DecodesUserRating(t *testing.T) {
	getSongCalls := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/search3":
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
		case "/rest/getSong":
			getSongCalls++
			if got := r.URL.Query().Get("id"); got != "song-1" {
				t.Fatalf("id = %q, want %q", got, "song-1")
			}

			writeJSON(t, w, map[string]any{
				"subsonic-response": map[string]any{
					"status":  "ok",
					"version": "1.16.1",
					"song": map[string]any{
						"id":         "song-1",
						"userRating": 4,
					},
				},
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
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
	if getSongCalls != 1 {
		t.Fatalf("getSongCalls = %d, want 1", getSongCalls)
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

func TestSearchSongsByTitleFallback_LogsInAndUsesNativeSongEndpoint(t *testing.T) {
	loginCalls := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/login":
			loginCalls++
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if got := body["username"]; got != "admin" {
				t.Fatalf("username = %q, want %q", got, "admin")
			}
			if got := body["password"]; got != "password" {
				t.Fatalf("password = %q, want %q", got, "password")
			}

			writeJSON(t, w, map[string]any{
				"token": "native-jwt",
			})
		case "/song":
			if got := r.Header.Get(nativeAuthorizationHeader); got != "Bearer native-jwt" {
				t.Fatalf("%s = %q, want %q", nativeAuthorizationHeader, got, "Bearer native-jwt")
			}
			if got := r.URL.Query().Get("title"); got != "Track Title" {
				t.Fatalf("title = %q, want %q", got, "Track Title")
			}
			if got := r.URL.Query().Get("_end"); got != "2" {
				t.Fatalf("_end = %q, want %q", got, "2")
			}

			writeJSON(t, w, []map[string]any{
				{
					"id":             "song-2",
					"path":           "Artist/Album/Track Title.mp3",
					"rating":         5,
					"mbzRecordingID": "mbid-2",
					"artist":         "Artist",
					"album":          "Album",
				},
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	})

	results, err := client.SearchSongsByTitleFallback(context.Background(), "Track Title", 2)
	if err != nil {
		t.Fatalf("SearchSongsByTitleFallback() error = %v", err)
	}
	if loginCalls != 1 {
		t.Fatalf("loginCalls = %d, want 1", loginCalls)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].UserRating != 5 {
		t.Fatalf("results[0].UserRating = %d, want 5", results[0].UserRating)
	}
	if results[0].MusicBrainzID != "mbid-2" {
		t.Fatalf("results[0].MusicBrainzID = %q, want %q", results[0].MusicBrainzID, "mbid-2")
	}
}

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	httpClient := server.Client()
	restyClient := resty.NewWithClient(httpClient).
		SetBaseURL(server.URL).
		SetHeader("Accept", "application/json")

	return &Client{
		baseURL:  server.URL,
		username: "admin",
		password: "password",
		http:     restyClient,
		native:   newNativeSearchClient(server.URL, "admin", "password", httpClient, log.New(io.Discard)),
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
