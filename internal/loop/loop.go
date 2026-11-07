package loop

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/kireevroi/spotify-tg/internal/spotify"
)

// Publisher shows the current track somewhere on Telegram.
type Publisher interface {
	Playing(ctx context.Context, t *spotify.Track) error
	Idle(ctx context.Context, last *spotify.Track) error
}

// Backoff is implemented by errors that carry a server-requested wait.
type Backoff interface{ Backoff() time.Duration }

type Config struct {
	PollInterval time.Duration
	MinProgress  time.Duration
	IdleAfter    time.Duration
	ResetOnExit  bool
}

type Source interface {
	CurrentlyPlaying(ctx context.Context) (spotify.Playback, error)
}

func Run(ctx context.Context, cfg Config, src Source, pub Publisher, log *slog.Logger) error {
	var (
		published string
		idle      bool
		idleSince time.Time
		last      *spotify.Track
		wait      time.Duration
	)

	defer func() {
		if !cfg.ResetOnExit || idle {
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := pub.Idle(c, last); err != nil {
			log.Warn("reset on exit failed", "err", err)
		} else {
			log.Info("reset to idle on exit")
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		wait = cfg.PollInterval

		pb, err := src.CurrentlyPlaying(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			wait = backoff(err, wait)
			log.Warn("spotify poll failed", "err", err, "retry_in", wait)
			continue
		}

		if pb.Playing && pb.Track != nil {
			idleSince = time.Time{}
			if pb.Track.ID == published && !idle {
				continue
			}
			if time.Duration(pb.ProgressMS)*time.Millisecond < cfg.MinProgress {
				continue
			}
			if err := pub.Playing(ctx, pb.Track); err != nil {
				wait = backoff(err, wait)
				log.Warn("publish failed", "err", err, "retry_in", wait)
				continue
			}
			published, idle, last = pb.Track.ID, false, pb.Track
			log.Info("now playing", "title", pb.Track.Title, "artists", pb.Track.Artists)
			continue
		}

		if idle {
			continue
		}
		if idleSince.IsZero() {
			idleSince = time.Now()
		}
		if time.Since(idleSince) < cfg.IdleAfter {
			continue
		}
		if err := pub.Idle(ctx, last); err != nil {
			wait = backoff(err, wait)
			log.Warn("idle publish failed", "err", err, "retry_in", wait)
			continue
		}
		idle, published = true, ""
		log.Info("idle")
	}
}

func backoff(err error, def time.Duration) time.Duration {
	var b Backoff
	if errors.As(err, &b) && b.Backoff() > def {
		return b.Backoff() + time.Second
	}
	return def
}
