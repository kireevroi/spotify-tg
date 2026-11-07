package spotify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kireevroi/spotify-tg/internal/store"
)

type rewrite struct{ target *url.URL }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme, req.URL.Host = r.target.Scheme, r.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestCurrentlyPlayingRefreshes(t *testing.T) {
	var refreshes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/token":
			refreshes.Add(1)
			io.WriteString(w, `{"access_token":"new","refresh_token":"rotated","expires_in":3600}`)
		case "/v1/me/player/currently-playing":
			if r.Header.Get("Authorization") != "Bearer new" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"is_playing":true,"progress_ms":12000,"currently_playing_type":"track",
				"item":{"id":"abc","name":"Song","external_urls":{"spotify":"https://open.spotify.com/track/abc"},
				"artists":[{"name":"A"},{"name":"B"}],
				"album":{"name":"Al","images":[{"url":"small","width":64},{"url":"big","width":640}]}}}`)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	path := filepath.Join(t.TempDir(), "tok.json")
	store.WriteJSON(path, token{AccessToken: "old", RefreshToken: "r1", Expiry: time.Now().Add(time.Hour)})

	c := &Client{ClientID: "id", ClientSecret: "s", TokenPath: path, HTTP: &http.Client{Transport: rewrite{u}}}
	pb, err := c.CurrentlyPlaying(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pb.Track == nil || pb.Track.Artists != "A, B" || pb.Track.CoverURL != "big" || !pb.Playing {
		t.Fatalf("bad parse: %+v %+v", pb, pb.Track)
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes.Load())
	}
	var saved token
	store.ReadJSON(path, &saved)
	if saved.RefreshToken != "rotated" || saved.AccessToken != "new" {
		t.Fatalf("rotated token not saved: %+v", saved)
	}
}

func TestNothingPlaying(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	path := filepath.Join(t.TempDir(), "tok.json")
	store.WriteJSON(path, token{AccessToken: "x", RefreshToken: "r", Expiry: time.Now().Add(time.Hour)})
	c := &Client{TokenPath: path, HTTP: &http.Client{Transport: rewrite{u}}}
	pb, err := c.CurrentlyPlaying(context.Background())
	if err != nil || pb.Track != nil {
		t.Fatalf("want empty playback, got %+v %v", pb, err)
	}
}
