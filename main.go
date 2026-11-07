package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"

	"github.com/kireevroi/spotify-tg/internal/bio"
	"github.com/kireevroi/spotify-tg/internal/channel"
	"github.com/kireevroi/spotify-tg/internal/loop"
	"github.com/kireevroi/spotify-tg/internal/spotify"
	"github.com/kireevroi/spotify-tg/internal/term"
)

const usage = `usage: nowplaying <command>

commands:
  spotify-auth   one-time Spotify login, stores a refresh token in $DATA_DIR
  tg-login       one-time Telegram user login for bio mode, stores a session in $DATA_DIR
  run bio        keep your Telegram bio in sync with Spotify
  run channel    keep a channel post in sync with Spotify`

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	var err error
	switch {
	case args[0] == "spotify-auth":
		err = spotifyAuth(ctx, log)
	case args[0] == "tg-login":
		err = tgLogin(ctx, log)
	case args[0] == "run" && len(args) == 2 && args[1] == "bio":
		err = runBio(ctx, log)
	case args[0] == "run" && len(args) == 2 && args[1] == "channel":
		err = runChannel(ctx, log)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func dataPath(name string) string { return filepath.Join(env("DATA_DIR", "/data"), name) }

func spotifyClient() (*spotify.Client, error) {
	c := &spotify.Client{
		ClientID:     os.Getenv("SPOTIFY_CLIENT_ID"),
		ClientSecret: os.Getenv("SPOTIFY_CLIENT_SECRET"),
		RedirectURI:  env("SPOTIFY_REDIRECT_URI", "http://127.0.0.1:8888/callback"),
		TokenPath:    dataPath("spotify-token.json"),
		HTTP:         &http.Client{Timeout: 15 * time.Second},
	}
	if c.ClientID == "" || c.ClientSecret == "" {
		return nil, errors.New("SPOTIFY_CLIENT_ID and SPOTIFY_CLIENT_SECRET are required")
	}
	return c, nil
}

func loopConfig() loop.Config {
	return loop.Config{
		PollInterval: envDur("POLL_INTERVAL", 20*time.Second),
		MinProgress:  envDur("MIN_PROGRESS", 10*time.Second),
		IdleAfter:    envDur("IDLE_AFTER", 60*time.Second),
		ResetOnExit:  envBool("RESET_ON_EXIT", true),
	}
}

func spotifyAuth(ctx context.Context, log *slog.Logger) error {
	c, err := spotifyClient()
	if err != nil {
		return err
	}
	authURL, state := c.AuthCodeURL()
	fmt.Printf("\n1. Open this URL and approve access:\n\n%s\n\n", authURL)
	fmt.Println("2. The browser then goes to", c.RedirectURI)
	fmt.Println("   If that page loads, you're done. If it fails to load (remote server), copy the full URL from the address bar and paste it here.")
	fmt.Print("\nRedirected URL: ")

	codes := make(chan string, 2)
	errs := make(chan error, 2)

	if ru, err := url.Parse(c.RedirectURI); err == nil && (ru.Hostname() == "127.0.0.1" || ru.Hostname() == "localhost") {
		mux := http.NewServeMux()
		mux.HandleFunc(ru.Path, func(w http.ResponseWriter, r *http.Request) {
			code, err := codeFromQuery(r.URL.Query(), state)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			fmt.Fprintln(w, "Spotify connected, you can close this tab.")
			codes <- code
		})
		ln, err := net.Listen("tcp", ":"+ru.Port())
		if err == nil {
			srv := &http.Server{Handler: mux}
			go srv.Serve(ln)
			defer srv.Close()
		}
	}

	go func() {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			errs <- fmt.Errorf("read redirected URL: %w", err)
			return
		}
		u, err := url.Parse(strings.TrimSpace(line))
		if err != nil {
			errs <- err
			return
		}
		code, err := codeFromQuery(u.Query(), state)
		if err != nil {
			errs <- err
			return
		}
		codes <- code
	}()

	var code string
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errs:
		return err
	case code = <-codes:
	}
	if err := c.Exchange(ctx, code); err != nil {
		return err
	}
	pb, err := c.CurrentlyPlaying(ctx)
	if err != nil {
		return fmt.Errorf("token saved but test call failed: %w", err)
	}
	fmt.Println()
	if pb.Track != nil {
		log.Info("spotify connected", "now_playing", pb.Track.Artists+" — "+pb.Track.Title)
	} else {
		log.Info("spotify connected, nothing playing right now")
	}
	return nil
}

func codeFromQuery(q url.Values, state string) (string, error) {
	if e := q.Get("error"); e != "" {
		return "", fmt.Errorf("spotify denied access: %s", e)
	}
	if q.Get("state") != state {
		return "", errors.New("state mismatch, start spotify-auth again")
	}
	if q.Get("code") == "" {
		return "", errors.New("no code in URL")
	}
	return q.Get("code"), nil
}

func tgClient() (*telegram.Client, error) {
	id, err := strconv.Atoi(os.Getenv("TG_API_ID"))
	if err != nil || id == 0 {
		return nil, errors.New("TG_API_ID is required (from my.telegram.org)")
	}
	hash := os.Getenv("TG_API_HASH")
	if hash == "" {
		return nil, errors.New("TG_API_HASH is required (from my.telegram.org)")
	}
	return telegram.NewClient(id, hash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: dataPath("tg-session.json")},
	}), nil
}

type termAuth struct{ in *bufio.Reader }

func (a termAuth) prompt(label string) (string, error) {
	fmt.Print(label)
	s, err := a.in.ReadString('\n')
	return strings.TrimSpace(s), err
}

func (a termAuth) Phone(context.Context) (string, error) {
	if p := os.Getenv("TG_PHONE"); p != "" {
		return p, nil
	}
	return a.prompt("Phone number (international format): ")
}

func (a termAuth) Code(context.Context, *tg.AuthSentCode) (string, error) {
	return a.prompt("Login code from Telegram: ")
}

func (a termAuth) Password(context.Context) (string, error) {
	restore := term.NoEcho(int(os.Stdin.Fd()))
	defer func() { restore(); fmt.Println() }()
	return a.prompt("2FA password: ")
}

func (termAuth) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error { return nil }

func (termAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("this phone has no Telegram account")
}

func tgLogin(ctx context.Context, log *slog.Logger) error {
	client, err := tgClient()
	if err != nil {
		return err
	}
	return client.Run(ctx, func(ctx context.Context) error {
		flow := auth.NewFlow(termAuth{in: bufio.NewReader(os.Stdin)}, auth.SendCodeOptions{})
		if err := client.Auth().IfNecessary(ctx, flow); err != nil {
			return err
		}
		self, err := client.Self(ctx)
		if err != nil {
			return err
		}
		log.Info("telegram logged in", "user", self.Username, "id", self.ID)

		if os.Getenv("BIO_DEFAULT") != "" {
			return nil
		}
		if _, err := os.Stat(dataPath("bio-default.txt")); err == nil {
			return nil
		}
		full, err := client.API().UsersGetFullUser(ctx, &tg.InputUserSelf{})
		if err != nil {
			return err
		}
		return bio.SaveDefault(dataPath("bio-default.txt"), full.FullUser.About, log)
	})
}

func runBio(ctx context.Context, log *slog.Logger) error {
	sp, err := spotifyClient()
	if err != nil {
		return err
	}
	client, err := tgClient()
	if err != nil {
		return err
	}
	log = log.With("mode", "bio")
	return client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if !status.Authorized {
			return errors.New("telegram session not authorized, run tg-login first")
		}
		pub := &bio.Publisher{
			API:         client.API(),
			Format:      env("BIO_FORMAT", "🎧 {artist} — {title} {link}"),
			MaxLen:      envInt("BIO_MAX_LEN", 70),
			Default:     os.Getenv("BIO_DEFAULT"),
			DefaultPath: dataPath("bio-default.txt"),
			Log:         log,
		}
		if err := pub.Init(ctx); err != nil {
			return err
		}
		log.Info("started", "default_bio", pub.Default)
		return loop.Run(ctx, loopConfig(), sp, pub, log)
	})
}

func runChannel(ctx context.Context, log *slog.Logger) error {
	sp, err := spotifyClient()
	if err != nil {
		return err
	}
	pub := &channel.Publisher{
		Token:     os.Getenv("TG_BOT_TOKEN"),
		Chat:      os.Getenv("TG_CHANNEL"),
		Strategy:  channel.Strategy(env("CHANNEL_STRATEGY", "edit")),
		Pin:       envBool("CHANNEL_PIN", true),
		IdleText:  env("CHANNEL_IDLE_TEXT", "⏸ Not listening right now"),
		StatePath: dataPath("channel-state.json"),
		HTTP:      &http.Client{Timeout: 20 * time.Second},
		Log:       log.With("mode", "channel"),
	}
	if pub.Token == "" || pub.Chat == "" {
		return errors.New("TG_BOT_TOKEN and TG_CHANNEL are required")
	}
	if pub.Strategy != channel.StrategyEdit && pub.Strategy != channel.StrategyRepost {
		return fmt.Errorf("CHANNEL_STRATEGY must be edit or repost, got %q", pub.Strategy)
	}
	pub.Log.Info("started", "channel", pub.Chat, "strategy", pub.Strategy)
	return loop.Run(ctx, loopConfig(), sp, pub, pub.Log)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if v, err := strconv.ParseBool(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func envDur(k string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func logLevel() slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(env("LOG_LEVEL", "info"))); err != nil {
		return slog.LevelInfo
	}
	return l
}
