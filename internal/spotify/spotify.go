package spotify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kireevroi/spotify-tg/internal/store"
)

const (
	authURL  = "https://accounts.spotify.com/authorize"
	tokenURL = "https://accounts.spotify.com/api/token"
	apiBase  = "https://api.spotify.com/v1"
	scopes   = "user-read-currently-playing user-read-playback-state"
)

type Track struct {
	ID       string
	Title    string
	Artists  string
	Album    string
	CoverURL string
	URL      string
	Episode  bool
}

type Playback struct {
	Playing    bool
	ProgressMS int
	Track      *Track
}

// RateLimitError means Spotify asked us to back off.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("spotify rate limited, retry after %s", e.RetryAfter)
}

func (e *RateLimitError) Backoff() time.Duration { return e.RetryAfter }

type token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
}

type Client struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	TokenPath    string
	HTTP         *http.Client

	cached token
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// AuthCodeURL returns the consent URL and the state to verify on callback.
func (c *Client) AuthCodeURL() (string, string) {
	b := make([]byte, 16)
	rand.Read(b)
	state := hex.EncodeToString(b)
	q := url.Values{
		"client_id":     {c.ClientID},
		"response_type": {"code"},
		"redirect_uri":  {c.RedirectURI},
		"scope":         {scopes},
		"state":         {state},
	}
	return authURL + "?" + q.Encode(), state
}

// Exchange trades an auth code for tokens and persists them.
func (c *Client) Exchange(ctx context.Context, code string) error {
	t, err := c.tokenRequest(ctx, url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {c.RedirectURI},
	})
	if err != nil {
		return err
	}
	if t.RefreshToken == "" {
		return errors.New("spotify returned no refresh token")
	}
	unlock, err := store.Lock(c.TokenPath + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	c.cached = t
	return store.WriteJSON(c.TokenPath, t)
}

// accessToken returns a valid token, refreshing under a cross-process lock.
func (c *Client) accessToken(ctx context.Context, force bool) (string, error) {
	if !force && c.cached.AccessToken != "" && time.Until(c.cached.Expiry) > time.Minute {
		return c.cached.AccessToken, nil
	}
	unlock, err := store.Lock(c.TokenPath + ".lock")
	if err != nil {
		return "", err
	}
	defer unlock()

	var t token
	ok, err := store.ReadJSON(c.TokenPath, &t)
	if err != nil {
		return "", err
	}
	if !ok || t.RefreshToken == "" {
		return "", fmt.Errorf("no spotify token at %s, run spotify-auth first", c.TokenPath)
	}
	// Another container may have refreshed already; on a forced refresh only reuse a token newer than ours.
	fresh := t.AccessToken != "" && time.Until(t.Expiry) > time.Minute
	if fresh && (!force || t.AccessToken != c.cached.AccessToken) {
		c.cached = t
		return t.AccessToken, nil
	}

	nt, err := c.tokenRequest(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
	})
	if err != nil {
		return "", err
	}
	if nt.RefreshToken == "" {
		nt.RefreshToken = t.RefreshToken
	}
	if err := store.WriteJSON(c.TokenPath, nt); err != nil {
		return "", err
	}
	c.cached = nt
	return nt.AccessToken, nil
}

func (c *Client) tokenRequest(ctx context.Context, form url.Values) (token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(c.ClientID, c.ClientSecret)
	resp, err := c.http().Do(req)
	if err != nil {
		return token{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return token{}, fmt.Errorf("spotify token: %s: %s", resp.Status, body)
	}
	var r struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return token{}, err
	}
	return token{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(r.ExpiresIn) * time.Second),
	}, nil
}

type apiImage struct {
	URL   string `json:"url"`
	Width int    `json:"width"`
}

type apiItem struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ExternalURLs struct {
		Spotify string `json:"spotify"`
	} `json:"external_urls"`
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
	Album struct {
		Name   string     `json:"name"`
		Images []apiImage `json:"images"`
	} `json:"album"`
	Images []apiImage `json:"images"`
	Show   struct {
		Name      string     `json:"name"`
		Publisher string     `json:"publisher"`
		Images    []apiImage `json:"images"`
	} `json:"show"`
}

// CurrentlyPlaying returns what the user is playing; Track is nil when nothing is.
func (c *Client) CurrentlyPlaying(ctx context.Context) (Playback, error) {
	resp, err := c.get(ctx, apiBase+"/me/player/currently-playing?additional_types=track,episode")
	if err != nil {
		return Playback{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return Playback{}, nil
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Playback{}, fmt.Errorf("spotify currently-playing: %s: %s", resp.Status, body)
	}
	var r struct {
		IsPlaying  bool     `json:"is_playing"`
		ProgressMS int      `json:"progress_ms"`
		Type       string   `json:"currently_playing_type"`
		Item       *apiItem `json:"item"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Playback{}, err
	}
	pb := Playback{Playing: r.IsPlaying, ProgressMS: r.ProgressMS}
	if r.Item == nil || r.Item.ID == "" {
		return pb, nil
	}
	it := r.Item
	t := &Track{ID: it.ID, Title: it.Name, URL: it.ExternalURLs.Spotify}
	if r.Type == "episode" {
		t.Episode = true
		t.Artists = it.Show.Publisher
		t.Album = it.Show.Name
		t.CoverURL = biggest(it.Images)
		if t.CoverURL == "" {
			t.CoverURL = biggest(it.Show.Images)
		}
	} else {
		names := make([]string, 0, len(it.Artists))
		for _, a := range it.Artists {
			names = append(names, a.Name)
		}
		t.Artists = strings.Join(names, ", ")
		t.Album = it.Album.Name
		t.CoverURL = biggest(it.Album.Images)
	}
	pb.Track = t
	return pb, nil
}

func (c *Client) get(ctx context.Context, u string) (*http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := c.accessToken(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := c.http().Do(req)
		if err != nil {
			return nil, err
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			resp.Body.Close()
			continue
		case http.StatusTooManyRequests:
			resp.Body.Close()
			secs, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
			if secs <= 0 {
				secs = 30
			}
			return nil, &RateLimitError{RetryAfter: time.Duration(secs) * time.Second}
		}
		return resp, nil
	}
	return nil, errors.New("spotify: unauthorized after token refresh")
}

func biggest(imgs []apiImage) string {
	best := ""
	w := -1
	for _, im := range imgs {
		if im.Width > w {
			best, w = im.URL, im.Width
		}
	}
	return best
}
