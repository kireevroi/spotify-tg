package loop

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/kireevroi/spotify-tg/internal/spotify"
)

type fakeSource struct {
	mu    sync.Mutex
	steps []spotify.Playback
	i     int
}

func (f *fakeSource) CurrentlyPlaying(context.Context) (spotify.Playback, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pb := f.steps[min(f.i, len(f.steps)-1)]
	f.i++
	return pb, nil
}

type fakePub struct {
	mu     sync.Mutex
	events []string
}

func (f *fakePub) Playing(_ context.Context, t *spotify.Track) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "play:"+t.ID)
	return nil
}

func (f *fakePub) Idle(context.Context, *spotify.Track) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "idle")
	return nil
}

func playing(id string, progress int) spotify.Playback {
	return spotify.Playback{Playing: true, ProgressMS: progress, Track: &spotify.Track{ID: id}}
}

func TestRun(t *testing.T) {
	src := &fakeSource{steps: []spotify.Playback{
		playing("a", 2000),  // too early, skipped
		playing("a", 15000), // published
		playing("a", 30000), // same track, no-op
		{},                  // paused
		playing("a", 31000), // resumed before idle timeout, no-op
		playing("b", 500),   // skip in progress
		playing("c", 12000), // published
		{}, {}, {},          // idle after timeout
		playing("c", 40000), // resumed after idle, published again
	}}
	pub := &fakePub{}
	cfg := Config{PollInterval: 5 * time.Millisecond, MinProgress: 10 * time.Second, IdleAfter: 8 * time.Millisecond, ResetOnExit: true}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	Run(ctx, cfg, src, pub, slog.New(slog.NewTextHandler(io.Discard, nil)))

	want := []string{"play:a", "play:c", "idle", "play:c", "idle"}
	if len(pub.events) != len(want) {
		t.Fatalf("events = %v, want %v", pub.events, want)
	}
	for i := range want {
		if pub.events[i] != want[i] {
			t.Fatalf("events = %v, want %v", pub.events, want)
		}
	}
}
