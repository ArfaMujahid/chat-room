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
- **Single binary** — the web UI is embedded via `go:embed`; Postgres is the only
  external dependency.

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

## Run the demo

The fastest path — Postgres + the chat server, one command:

```sh
docker compose up --build
```

Then open <http://localhost:8080> in two or three browser windows, **register an
account** (or log in) in each, join the same room, and type. The server applies its
schema migrations on startup, so there is no separate setup step.

### Run against your own Postgres

```sh
go run ./cmd/chat \
  --addr :8080 \
  --db-url "postgres://user:pass@localhost:5432/chat?sslmode=disable"
```

Run `go run ./cmd/chat --help` for all flags (message-size cap, send-buffer depth,
ping interval, history limit, room cap).

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
