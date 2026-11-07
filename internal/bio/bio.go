package bio

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/kireevroi/spotify-tg/internal/format"
	"github.com/kireevroi/spotify-tg/internal/spotify"
)

type Publisher struct {
	API         *tg.Client
	Format      string
	MaxLen      int
	Default     string
	DefaultPath string
	Log         *slog.Logger

	current     string
	haveCurrent bool
}

type floodErr struct{ wait time.Duration }

func (e *floodErr) Error() string { return fmt.Sprintf("telegram FLOOD_WAIT %s", e.wait) }

func (e *floodErr) Backoff() time.Duration { return e.wait }

// Init resolves the default bio and reads the current one.
func (p *Publisher) Init(ctx context.Context) error {
	full, err := p.API.UsersGetFullUser(ctx, &tg.InputUserSelf{})
	if err != nil {
		return fmt.Errorf("get own profile: %w", err)
	}
	p.current, p.haveCurrent = full.FullUser.About, true

	if p.Default != "" {
		return nil
	}
	if b, err := os.ReadFile(p.DefaultPath); err == nil {
		p.Default = strings.TrimRight(string(b), "\n")
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return SaveDefault(p.DefaultPath, p.current, p.Log)
}

// SaveDefault stores the bio to restore when nothing is playing.
func SaveDefault(path, bio string, log *slog.Logger) error {
	if err := os.WriteFile(path, []byte(bio+"\n"), 0o600); err != nil {
		return err
	}
	log.Info("saved default bio", "path", path, "bio", bio)
	return nil
}

func (p *Publisher) Playing(ctx context.Context, t *spotify.Track) error {
	return p.set(ctx, format.Fit(p.Format, t, p.MaxLen))
}

func (p *Publisher) Idle(ctx context.Context, _ *spotify.Track) error {
	return p.set(ctx, p.Default)
}

func (p *Publisher) set(ctx context.Context, about string) error {
	if p.haveCurrent && about == p.current {
		return nil
	}
	req := &tg.AccountUpdateProfileRequest{}
	req.SetAbout(about)
	if _, err := p.API.AccountUpdateProfile(ctx, req); err != nil {
		if d, ok := tgerr.AsFloodWait(err); ok {
			return &floodErr{wait: d}
		}
		if tgerr.Is(err, "ABOUT_TOO_LONG") {
			return fmt.Errorf("bio too long for this account, lower BIO_MAX_LEN: %w", err)
		}
		return err
	}
	p.current, p.haveCurrent = about, true
	return nil
}
