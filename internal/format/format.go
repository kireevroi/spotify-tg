package format

import (
	"html"
	"strings"
	"unicode/utf16"

	"github.com/kireevroi/spotify-tg/internal/spotify"
)

// Template fills {title}, {artist}, {album}, {url} and {link} placeholders.
func Template(tpl string, t *spotify.Track) string {
	return strings.NewReplacer(
		"{title}", t.Title,
		"{artist}", t.Artists,
		"{album}", t.Album,
		"{url}", t.URL,
		"{link}", ShortLink(t.URL),
	).Replace(tpl)
}

// ShortLink strips the scheme; Telegram still makes it clickable.
func ShortLink(u string) string {
	return strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
}

// Fit renders tpl within max, cutting the text before a {link}/{url} and keeping the link whole when possible.
func Fit(tpl string, t *spotify.Track, max int) string {
	full := Template(tpl, t)
	if max <= 0 || utf16Len(full) <= max {
		return full
	}
	ph, link := "{link}", ShortLink(t.URL)
	i := strings.Index(tpl, ph)
	if i < 0 {
		ph, link = "{url}", t.URL
		i = strings.Index(tpl, ph)
	}
	if i < 0 || link == "" {
		return Truncate(full, max)
	}
	pre := Template(tpl[:i], t)
	post := Template(tpl[i+len(ph):], t)
	room := max - utf16Len(link) - utf16Len(post) - 1
	if room < 12 {
		noLink := strings.Join(strings.Fields(strings.Replace(tpl, ph, "", 1)), " ")
		return Truncate(Template(noLink, t), max)
	}
	return Truncate(strings.TrimRight(pre, " "), room) + " " + link + post
}

// Truncate cuts s to max UTF-16 code units, ending with an ellipsis when cut.
func Truncate(s string, max int) string {
	if max <= 0 || utf16Len(s) <= max {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && utf16Len(string(runes))+1 > max {
		runes = runes[:len(runes)-1]
	}
	return strings.TrimRight(string(runes), " -—·,") + "…"
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// Caption renders the HTML caption for the channel post.
func Caption(t *spotify.Track) string {
	icon := "🎧"
	if t.Episode {
		icon = "🎙"
	}
	var b strings.Builder
	b.WriteString(icon + " <b>" + html.EscapeString(t.Title) + "</b>\n")
	b.WriteString(html.EscapeString(t.Artists))
	if t.Album != "" {
		b.WriteString(" · <i>" + html.EscapeString(t.Album) + "</i>")
	}
	if t.URL != "" {
		b.WriteString("\n\n<a href=\"" + html.EscapeString(t.URL) + "\">Listen on Spotify</a>")
	}
	return b.String()
}

// IdleCaption renders the caption shown when nothing is playing.
func IdleCaption(idleText string, last *spotify.Track) string {
	s := html.EscapeString(idleText)
	if last != nil {
		s += "\n\nLast played: <b>" + html.EscapeString(last.Title) + "</b> — " + html.EscapeString(last.Artists)
		if last.URL != "" {
			s += "\n<a href=\"" + html.EscapeString(last.URL) + "\">Listen on Spotify</a>"
		}
	}
	return s
}
