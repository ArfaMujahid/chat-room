# Real-Time Chat Server

[![CI](https://github.com/ArfaMujahid/chat-room/actions/workflows/ci.yml/badge.svg)](https://github.com/ArfaMujahid/chat-room/actions/workflows/ci.yml)

A real-time, multi-room chat server in Go. Users connect over a **WebSocket**, pick a
display name, join **named public rooms**, and exchange messages that appear instantly
for everyone in the room. Message history is **persisted to Postgres** so it survives
restarts and new joiners see recent context.

This is **Project 3 of 3** (Scraper → URL Shortener → Chat) — the capstone. See
[`chat-architecture.md`](chat-architecture.md) for the full architecture & requirements
spec and [`CODING-STANDARDS.md`](CODING-STANDARDS.md) for the Go conventions every
change is reviewed against.

> **Status:** complete and verified end-to-end. Real-time multi-room chat with
> **username/password accounts** (bcrypt, server-side sessions), Postgres-backed
> history, a polished embedded UI, and graceful shutdown. The build is
> `gofmt`/`vet`/`golangci-lint`/`-race`-clean with no known vulns, and the full flow
> (register → chat → logout → login) is verified against a live Postgres.

## Highlights

- **Hub-and-spoke concurrency** — a single actor goroutine owns the rooms/clients map;
  no mutexes on the broadcast path (race-free by construction).
- **Slow-consumer isolation** — the hub broadcasts with a non-blocking send; a client
  whose buffer is full is dropped, so one slow client never stalls the room.
- **Hot/cold split** — messages are delivered on the hot path and persisted
  asynchronously on the cold path, so delivery latency is independent of the DB.
- **Scaling seam** — broadcasting goes through a `MessageBus` interface; a Redis
  Pub/Sub backplane drops in for multi-server scaling (v2) without touching the hub.
- **Real accounts** — username/password login with bcrypt-hashed passwords and
  revocable, server-side sessions (token hash stored, not the token); `/ws` and the
  rooms API require a valid session.
- **Single binary, zero setup** — the web UI is embedded via `go:embed` and storage
  defaults to an embedded SQLite file, so the binary runs with no external services.
  Point `--db-url` at Postgres for a multi-instance/production setup.

## Layout

```
cmd/chat/         composition root (config → wiring → serve → graceful shutdown)
internal/config   Config + Validate()
internal/message  the wire protocol (Envelope, Message)
internal/auth     username/password accounts + sessions (bcrypt, Postgres)
internal/store    MessageStore interface + Postgres impl
internal/bus      MessageBus interface + LocalBus (scaling seam)
internal/hub      the engine: hub actor, client (read/write pumps), room
internal/persist  async persistence worker (cold path)
internal/web      HTTP server, auth endpoints, /ws upgrade, embedded UI
migrations/       SQL schema migrations
```

## Build order (bottom-up; always runnable)

1. `config` → 2. `message` → 3. `store` (+ migration) → 4. `bus` → 5. `hub`
(room, client, hub actor + slow-client drop + race test) → 6. `persist` → 7. `web`
→ 8. `main` (✅ two-browser live demo) → 9. UI polish + Dockerfile + compose.

## Download & run (no setup)

Grab the binary for your OS/arch from the [latest release](https://github.com/ArfaMujahid/chat-room/releases/latest), then:

```sh
chmod +x chat_*                 # make it executable (macOS/Linux)
./chat_darwin_arm64             # listens on :8080, stores data in ./chat.db
```

Open <http://localhost:8080>, **register an account**, join a room, and chat. It
needs **nothing else** — messages and accounts are kept in an embedded SQLite file
(`chat.db`) that survives restarts.

> **macOS:** a downloaded, unsigned binary is quarantined by Gatekeeper. Allow it once
> with `xattr -d com.apple.quarantine ./chat_darwin_arm64` (or right-click → Open).

To see two users chatting live, open a second **browser profile or an incognito
window** (identity is a per-browser session cookie, so two tabs in the same browser
are the same account).

## Run from source

```sh
go run ./cmd/chat                       # embedded SQLite (./chat.db), zero setup
go run ./cmd/chat --addr :8080 \        # or point at your own Postgres
  --db-url "postgres://user:pass@localhost:5432/chat?sslmode=disable"
```

Or with Docker (Postgres + the server): `docker compose up --build`.

Run `go run ./cmd/chat --help` for all flags (database, message-size cap, send-buffer
depth, ping interval, history limit, room cap, session TTL, pprof debug address).

## Observability

- **Live metrics** — `GET /api/rooms` (authenticated) returns the connection count,
  active rooms with member counts, and the current message rate; the UI shows the
  online count and messages/sec in the header.
- **Profiling** — pass `--debug-addr 127.0.0.1:6060` to expose `pprof` on a separate
  local port (off by default, and never on the public chat port): e.g.
  `go tool pprof http://127.0.0.1:6060/debug/pprof/goroutine` to inspect goroutines.

## Develop

```sh
go build ./...
go vet ./...
gofmt -l .
golangci-lint run ./...
go test -race ./...
# Store integration test runs only when a database is provided:
CHAT_TEST_DB_URL="postgres://user:pass@localhost:5432/chat_test?sslmode=disable" \
  go test -race ./internal/store/...
```

## License

Personal learning project.
