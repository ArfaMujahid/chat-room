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

> **Status:** scaffold. The directory tree, package boundaries, core types, and
> consumer-side interfaces are in place and build clean. Implementations are stubbed
> with `TODO` markers and filled in following the build order below.

## Highlights

- **Hub-and-spoke concurrency** — a single actor goroutine owns the rooms/clients map;
  no mutexes on the broadcast path (race-free by construction).
- **Slow-consumer isolation** — the hub broadcasts with a non-blocking send; a client
  whose buffer is full is dropped, so one slow client never stalls the room.
- **Hot/cold split** — messages are delivered on the hot path and persisted
  asynchronously on the cold path, so delivery latency is independent of the DB.
- **Scaling seam** — broadcasting goes through a `MessageBus` interface; a Redis
  Pub/Sub backplane drops in for multi-server scaling (v2) without touching the hub.
- **Single binary** — the web UI is embedded via `go:embed`; Postgres is the only
  external dependency in v1.

## Layout

```
cmd/chat/         composition root (config → wiring → serve → graceful shutdown)
internal/config   Config + Validate()
internal/message  the wire protocol (Envelope, Message)
internal/session  session-cookie identity (UserID + display name)
internal/store    MessageStore interface + Postgres impl
internal/bus      MessageBus interface + LocalBus (v1) / RedisBus (v2 seam)
internal/hub      the engine: hub actor, client (read/write pumps), room
internal/persist  async persistence worker (cold path)
internal/web      HTTP server, /ws upgrade, REST routes, embedded UI
migrations/       SQL schema migrations
```

## Build order (bottom-up; always runnable)

1. `config` → 2. `message` → 3. `store` (+ migration) → 4. `bus` → 5. `hub`
(room, client, hub actor + slow-client drop + race test) → 6. `persist` → 7. `web`
→ 8. `main` (✅ two-browser live demo) → 9. UI polish + Dockerfile + compose.

## Quick start

```sh
# Build & vet (works today on the scaffold)
go build ./...
go vet ./...
gofmt -l .

# Run (once implemented) — Postgres connection comes from config/flags/env
go run ./cmd/chat --addr :8080 --db-url "postgres://localhost:5432/chat?sslmode=disable"
```

## License

Personal learning project.
