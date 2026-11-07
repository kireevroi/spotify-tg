package channel

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kireevroi/spotify-tg/internal/spotify"
)

type rewrite struct{ target *url.URL }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme, req.URL.Host = r.target.Scheme, r.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestEditFlow(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		mu.Lock()
		calls = append(calls, method)
		n := len(calls)
		mu.Unlock()
		switch {
		case method == "sendPhoto":
			io.WriteString(w, `{"ok":true,"result":{"message_id":42}}`)
		case method == "editMessageMedia" && n == 3:
			io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: message is not modified"}`)
		case method == "editMessageMedia" && n == 4:
			io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":7}}`)
		default:
			io.WriteString(w, `{"ok":true,"result":true}`)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	p := &Publisher{
		Token: "t", Chat: "@ch", Strategy: StrategyEdit, Pin: true, IdleText: "idle",
		StatePath: filepath.Join(t.TempDir(), "state.json"),
		HTTP:      &http.Client{Transport: rewrite{u}},
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx := context.Background()
	tr := &spotify.Track{ID: "1", Title: "T", Artists: "A", CoverURL: "https://i.scdn.co/x"}

	if err := p.Playing(ctx, tr); err != nil {
		t.Fatal(err)
	}
	if err := p.Playing(ctx, tr); err != nil {
		t.Fatal("not modified should be ok:", err)
	}
	err := p.Playing(ctx, tr)
	ae, ok := err.(*apiError)
	if !ok || ae.Backoff().Seconds() != 7 {
		t.Fatalf("want 429 with retry 7s, got %v", err)
	}
	if err := p.Idle(ctx, tr); err != nil {
		t.Fatal(err)
	}

	want := []string{"sendPhoto", "pinChatMessage", "editMessageMedia", "editMessageMedia", "editMessageCaption"}
	got, _ := json.Marshal(calls)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Fatalf("calls %s, want %s", got, exp)
	}

	p2 := &Publisher{StatePath: p.StatePath}
	if err := p2.load(); err != nil || p2.state.MessageID != 42 {
		t.Fatalf("state not persisted: %+v %v", p2.state, err)
	}
}
