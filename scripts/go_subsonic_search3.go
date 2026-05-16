package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/supersonic-app/go-subsonic/subsonic"
)

const (
	defaultQuery      = "Brænder"
	defaultResultSize = 10
	apiVersion        = "1.16.1"
	clientName        = "go-navidrome-sync-go-subsonic-test"
)

func main() {
	query := flag.String("query", defaultQuery, "Subsonic search3 query")
	limit := flag.Int("limit", defaultResultSize, "maximum number of song results")
	flag.Parse()

	baseURL := mustEnv("NAVIDROME_URL")
	user := mustEnv("NAVIDROME_USER")
	password := mustEnv("NAVIDROME_PASSWORD")

	client := &subsonic.Client{
		Client:              &http.Client{Timeout: 15 * time.Second},
		BaseUrl:             baseURL,
		User:                user,
		ClientName:          clientName,
		UseJSON:             true,
		RequestedAPIVersion: apiVersion,
	}

	if err := client.Authenticate(password); err != nil {
		if errors.Is(err, subsonic.ErrAuthenticationFailure) {
			log.Fatalf("authentication failed for %q", user)
		}
		log.Fatalf("authenticate: %v", err)
	}

	results, err := client.Search3(*query, map[string]string{
		"artistCount": "0",
		"albumCount":  "0",
		"songCount":   fmt.Sprintf("%d", *limit),
	})
	if err != nil {
		log.Fatalf("search3: %v", err)
	}

	if results == nil || len(results.Song) == 0 {
		fmt.Printf("query=%q songs=0\n", *query)
		return
	}

	fmt.Printf("query=%q songs=%d\n", *query, len(results.Song))
	for i, song := range results.Song {
		if song == nil {
			continue
		}
		fmt.Printf(
			"%d. title=%q artist=%q album=%q rating=%d mbid=%q path=%q id=%q\n",
			i+1,
			song.Title,
			song.Artist,
			song.Album,
			song.UserRating,
			song.MusicBrainzID,
			song.Path,
			song.ID,
		)
	}
}

func mustEnv(name string) string {
	value := os.Getenv(name)
	if value == "" {
		log.Fatalf("missing %s", name)
	}
	return value
}
