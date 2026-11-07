package format

import (
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/kireevroi/spotify-tg/internal/spotify"
)

func TestTruncate(t *testing.T) {
	s := Template("🎧 {artist} — {title}", &spotify.Track{Artists: "Boards of Canada", Title: "Roygbiv but with a very very long remastered deluxe title"})
	got := Truncate(s, 70)
	if n := len(utf16.Encode([]rune(got))); n > 70 {
		t.Fatalf("len %d > 70: %q", n, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("missing ellipsis: %q", got)
	}
	if Truncate("short", 70) != "short" {
		t.Fatal("short string changed")
	}
}

func TestCaptionEscapes(t *testing.T) {
	c := Caption(&spotify.Track{Title: "A <b> & C", Artists: "X", URL: "https://open.spotify.com/track/1"})
	if strings.Contains(c, "A <b>") || !strings.Contains(c, "A &lt;b&gt; &amp; C") {
		t.Fatalf("not escaped: %q", c)
	}
}

func TestFitKeepsLink(t *testing.T) {
	tr := &spotify.Track{Artists: "Папин Олимпос", Title: "Официантка", URL: "https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC"}
	tpl := "🎧 {artist} — {title} {link}"
	got := Fit(tpl, tr, 70)
	if n := len(utf16.Encode([]rune(got))); n > 70 {
		t.Fatalf("len %d > 70: %q", n, got)
	}
	if !strings.HasSuffix(got, " open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC") {
		t.Fatalf("link not kept whole: %q", got)
	}
	if got := Fit(tpl, tr, 140); got != "🎧 Папин Олимпос — Официантка open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC" {
		t.Fatalf("fits in 140 but changed: %q", got)
	}
	if got := Fit(tpl, tr, 40); strings.Contains(got, "spotify") || !strings.HasPrefix(got, "🎧 Папин") {
		t.Fatalf("link should be dropped when no room: %q", got)
	}
}
