package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kireevroi/spotify-tg/internal/format"
	"github.com/kireevroi/spotify-tg/internal/spotify"
	"github.com/kireevroi/spotify-tg/internal/store"
)

type Strategy string

const (
	StrategyEdit   Strategy = "edit"
	StrategyRepost Strategy = "repost"
)

type Publisher struct {
	Token     string
	Chat      string
	Strategy  Strategy
	Pin       bool
	IdleText  string
	StatePath string
	HTTP      *http.Client
	Log       *slog.Logger

	state state
	ready bool
}

type state struct {
	MessageID int `json:"message_id"`
}

type apiError struct {
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram bot api %d: %s", e.Code, e.Description)
}

func (e *apiError) Backoff() time.Duration { return e.RetryAfter }

func (e *apiError) has(s string) bool { return strings.Contains(strings.ToLower(e.Description), s) }

func (p *Publisher) load() error {
	if p.ready {
		return nil
	}
	if _, err := store.ReadJSON(p.StatePath, &p.state); err != nil {
		return err
	}
	p.ready = true
	return nil
}

func (p *Publisher) save() error { return store.WriteJSON(p.StatePath, p.state) }

func (p *Publisher) Playing(ctx context.Context, t *spotify.Track) error {
	if err := p.load(); err != nil {
		return err
	}
	caption := format.Caption(t)

	if t.CoverURL == "" {
		if p.state.MessageID == 0 {
			p.Log.Info("track has no cover and no post exists yet, skipping", "title", t.Title)
			return nil
		}
		return p.editCaption(ctx, caption)
	}

	if p.state.MessageID != 0 && p.Strategy == StrategyEdit {
		err := p.call(ctx, "editMessageMedia", map[string]any{
			"chat_id":    p.Chat,
			"message_id": p.state.MessageID,
			"media": map[string]any{
				"type":       "photo",
				"media":      t.CoverURL,
				"caption":    caption,
				"parse_mode": "HTML",
			},
		}, nil)
		if err == nil || isNotModified(err) {
			return nil
		}
		if !isGone(err) {
			return err
		}
		p.Log.Info("post was deleted, sending a new one")
		p.state.MessageID = 0
	}

	old := p.state.MessageID
	var msg struct {
		MessageID int `json:"message_id"`
	}
	if err := p.call(ctx, "sendPhoto", map[string]any{
		"chat_id":              p.Chat,
		"photo":                t.CoverURL,
		"caption":              caption,
		"parse_mode":           "HTML",
		"disable_notification": true,
	}, &msg); err != nil {
		return err
	}
	p.state.MessageID = msg.MessageID
	if err := p.save(); err != nil {
		return err
	}

	if old != 0 {
		if err := p.call(ctx, "deleteMessage", map[string]any{"chat_id": p.Chat, "message_id": old}, nil); err != nil && !isGone(err) {
			p.Log.Warn("delete previous post failed", "err", err)
		}
	}
	if p.Pin && p.Strategy == StrategyEdit {
		if err := p.call(ctx, "pinChatMessage", map[string]any{
			"chat_id":              p.Chat,
			"message_id":           msg.MessageID,
			"disable_notification": true,
		}, nil); err != nil {
			p.Log.Warn("pin failed", "err", err)
		}
	}
	return nil
}

func (p *Publisher) Idle(ctx context.Context, last *spotify.Track) error {
	if err := p.load(); err != nil {
		return err
	}
	if p.state.MessageID == 0 {
		return nil
	}
	return p.editCaption(ctx, format.IdleCaption(p.IdleText, last))
}

func (p *Publisher) editCaption(ctx context.Context, caption string) error {
	err := p.call(ctx, "editMessageCaption", map[string]any{
		"chat_id":    p.Chat,
		"message_id": p.state.MessageID,
		"caption":    caption,
		"parse_mode": "HTML",
	}, nil)
	if err == nil || isNotModified(err) {
		return nil
	}
	if isGone(err) {
		p.state.MessageID = 0
		return p.save()
	}
	return err
}

func (p *Publisher) call(ctx context.Context, method string, params any, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+p.Token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	hc := p.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		// Strip the URL so the bot token never lands in logs.
		return fmt.Errorf("telegram %s: request failed", method)
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool            `json:"ok"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("telegram %s: %s", method, resp.Status)
	}
	if !r.OK {
		return &apiError{Code: r.ErrorCode, Description: r.Description, RetryAfter: time.Duration(r.Parameters.RetryAfter) * time.Second}
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

func isNotModified(err error) bool {
	e, ok := err.(*apiError)
	return ok && e.has("message is not modified")
}

func isGone(err error) bool {
	e, ok := err.(*apiError)
	return ok && (e.has("message to edit not found") || e.has("message to delete not found") || e.has("message_id_invalid") || e.has("message can't be edited"))
}
