# nowplaying

Mirrors what you're playing on Spotify to Telegram. One Go binary, two modes, two containers:

- `bio` — your profile bio (as your user account, via MTProto).
- `channel` — a post in a channel (via a bot): cover art, track, artist, link.

Both share one Spotify token on a shared volume; refreshes are flock-guarded so the containers don't clobber each other's rotated refresh token.

## 1. Spotify app

1. https://developer.spotify.com/dashboard → Create app → Web API.
2. Redirect URI: `http://127.0.0.1:8888/callback` (Spotify rejects `localhost`, must be `127.0.0.1`).
3. Copy Client ID and Client Secret into `.env`.

Development Mode needs the app owner on Premium; fine for one personal account.

## 2. Telegram

**Bio mode:** create an app at https://my.telegram.org → API development tools → put `api_id` / `api_hash` into `.env`.

**Channel mode:** create a bot with @BotFather, add it to the channel as admin with *Post*, *Edit* and *Delete messages* rights, set `TG_CHANNEL` to `@username` or the `-100…` id. To show it on your profile: Telegram → Edit profile → Personal channel.

## 3. Run

```sh
cp .env.example .env   # fill it in

# CI publishes amd64+arm64 images to ghcr.io/kireevroi/spotify-tg
docker compose pull    # or: docker compose build

# one-time Spotify login: open the printed URL, approve, then paste the URL you got redirected to
docker compose run --rm -p 8888:8888 channel spotify-auth

# one-time Telegram login for bio mode: phone, code, 2FA password
docker compose run --rm bio tg-login

docker compose up -d
docker compose logs -f
```

On a headless server the redirect page will fail to load in your browser, that's expected: copy the full URL from the address bar and paste it into the prompt.

`tg-login` saves your current bio to `/data/bio-default.txt`; it's restored when nothing plays. Override with `BIO_DEFAULT`.

## Settings

| Var | Default | |
|---|---|---|
| `POLL_INTERVAL` | `20s` | Spotify poll interval |
| `MIN_PROGRESS` | `10s` | publish only after this much of a track played, filters skips |
| `IDLE_AFTER` | `60s` | paused/stopped this long → idle state |
| `RESET_ON_EXIT` | `true` | set idle state on container stop |
| `BIO_FORMAT` | `🎧 {artist} — {title} {link}` | also `{album}`, `{url}`; `{link}` is the track URL without `https://`, kept whole while artist/title get cut to fit |
| `BIO_MAX_LEN` | `70` | 140 with Telegram Premium |
| `CHANNEL_STRATEGY` | `edit` | `edit`: one pinned post edited in place. `repost`: silent new post per track, old one deleted, so it's always the latest post on your profile |
| `CHANNEL_PIN` | `true` | pin the post in `edit` mode |
| `CHANNEL_IDLE_TEXT` | `⏸ Not listening right now` | caption when idle, keeps last cover |

## Notes

- Bio updates are MTProto profile edits; `FLOOD_WAIT` is respected automatically. Updates happen only on track change, so it's low volume, but it's still a userbot on your real account.
- `gotd/td` is pinned to v0.135.0. The image builds with Go 1.25, so you can bump it with `go get github.com/gotd/td@latest`.
- Run tests: `go test ./...`
