# Real-Time Chat Server — Architecture & Requirements Specification

**Project 3 of 3** (Scraper → URL Shortener → **Chat**) · The capstone.
**Language:** Go · **Type:** WebSocket chat server with embedded web UI + Postgres

---

## 1. Overview

A real-time, multi-room chat server. Users connect over a **WebSocket**, pick a
display name, join **named public rooms**, and exchange messages that appear
instantly for everyone in the room. Message history is **persisted to Postgres** so
it survives restarts and new joiners see recent context. The server is **single-process
in v1**, but its broadcast path is built behind a `MessageBus` interface so a **Redis
Pub/Sub backplane** drops in for multi-server scaling (v2) without touching the core.

**What it adds over the first two projects (the capstone skills):**
- **Bidirectional, persistent connections** (WebSockets) — clients send *and* receive
  over one long-lived connection, vs the one-way SSE used before.
- **The hub-and-spoke concurrency pattern** — the textbook "many goroutines, one
  coordinator owning shared state" problem Go was built for.
- **The slow-consumer / backpressure problem** — one slow client must never block the
  broadcast to everyone else.

**What it reuses (the continuity):** session-based identity (from the shortener), the
hot-path/cold-path async split (deliver fast, persist asynchronously — also from the
shortener), `embed.FS` single-binary UI (from the scraper), graceful shutdown and the
worker/channel discipline (from both).

**The demo moment:** open three browser windows, join the same room, type in one and
watch it appear instantly in the others — plus a live presence list and connection
count. Universally impressive, and it's exactly what Go's goroutine-per-connection
model excels at.

---

## 2. Directory Architecture

```
chat/
├── go.mod
├── go.sum
├── README.md
├── Dockerfile                  # (later) multi-stage build
│
├── migrations/
│   └── 0001_init.sql           # messages table (+ indexes)
│
├── cmd/
│   └── chat/
│       └── main.go             # composition root: config, wiring, signals, run
│
└── internal/
    ├── config/
    │   └── config.go           # Config + Validate() (DB URL, addr, limits, ...)
    │
    ├── message/
    │   └── message.go          # the wire protocol: Envelope, Message types
    │
    ├── session/
    │   └── session.go          # session-cookie identity + display name (reused pattern)
    │
    ├── store/
    │   ├── store.go            # MessageStore interface (consumer-defined)
    │   └── postgres.go         # Postgres impl: SaveMessage, RecentByRoom
    │
    ├── bus/
    │   ├── bus.go              # MessageBus interface + LocalBus (v1, in-memory loopback)
    │   └── redis.go            # (v2) RedisBus — documented/stubbed, the scaling seam
    │
    ├── hub/
    │   ├── hub.go              # THE COORDINATOR: actor goroutine owning rooms/clients
    │   ├── client.go           # one connection: readPump + writePump, send channel
    │   └── room.go             # room membership set
    │
    ├── persist/
    │   └── persist.go          # async persistence worker (cold path → Postgres)
    │
    └── web/
        ├── server.go           # HTTP server, /ws upgrade endpoint, REST routes
        ├── server_test.go
        └── static/             # embedded chat UI via go:embed
            ├── index.html
            ├── style.css
            └── app.js          # WebSocket client: connect, send, render, reconnect
```

Same layout philosophy as the scraper (Lessons 7 & 12): `cmd/` for the binary,
`internal/` for everything else, organized **by domain**.

---

## 3. File-by-File Responsibilities

| File | Responsibility | Key Go concepts (lesson) |
|------|----------------|--------------------------|
| `cmd/chat/main.go` | **Composition root.** Parse flags → validate config → connect Postgres → build store/bus/hub/persister/server → `signal.NotifyContext` → serve → graceful shutdown. Thin. | DI, graceful shutdown (L12) |
| `internal/config/config.go` | `Config` (DB URL, listen addr, max message size, send-buffer size, ping interval, room limits, UI addr) + `Validate()` fail-fast. | structs, validation (L3, L12) |
| `internal/message/message.go` | The protocol: `Envelope` (typed client↔server frames: join/leave/message/history/presence/error) and the persisted `Message`. JSON-tagged. | structs, JSON tags (L3, L10) |
| `internal/session/session.go` | Resolve a stable `UserID` from a session cookie (minted once); carry the chosen display name. Same seam for real auth later. | middleware, context.WithValue (L6, L10) |
| `internal/store/store.go` | `MessageStore` **interface** (`SaveMessage`, `RecentByRoom`) defined where the hub/persister consume it. | consumer-side interfaces (L4) |
| `internal/store/postgres.go` | Postgres implementation: insert a message, fetch the last N for a room (ordered). Context-aware queries. | database/sql or pgx, context (L6, L10) |
| `internal/bus/bus.go` | `MessageBus` **interface** (`Publish`, `Subscribe`) + `LocalBus` (in-memory loopback for v1). **The scaling seam.** | interfaces, channels (L4, L6) |
| `internal/bus/redis.go` | (v2) `RedisBus`: publish to / subscribe from Redis Pub/Sub. Documented, not built in v1. | (the v2 story) |
| `internal/hub/hub.go` | **The engine.** Single actor goroutine owning the rooms→clients map; `select`s over register/unregister/inbound; broadcasts (non-blocking) and hands off to the persister. | actor pattern, channels, select, backpressure (L6) |
| `internal/hub/client.go` | One WebSocket connection wrapped: a buffered `send` channel, a `readPump` goroutine (WS→hub) and a `writePump` goroutine (hub→WS). **Single writer per connection.** | goroutines, connection ownership (L6) |
| `internal/hub/room.go` | A room's membership set (`map[*Client]struct{}`), owned by the hub goroutine (no mutex needed). | maps, struct{} sets (L3) |
| `internal/persist/persist.go` | Async worker: reads messages off a channel and writes them to the `MessageStore`, so delivery (hot path) never waits on the DB (cold path). | worker pattern, hot/cold split (L6) |
| `internal/web/server.go` | HTTP server (with timeouts), `GET /ws` WebSocket upgrade (origin check, session resolve, create Client), REST routes (`GET /api/rooms`), serves embedded UI. | http server, WS upgrade, embed (L10, L12) |
| `internal/web/static/*` | Vanilla WebSocket client UI: connect, send envelopes, render messages/presence, auto-reconnect. Embedded in the binary. | go:embed (L12) |

---

## 4. Functional Requirements

| ID | Requirement | Acceptance Criteria |
|----|-------------|---------------------|
| **FR-1** | Establish a WebSocket connection. | A browser connects to `/ws`, the connection upgrades successfully, and stays open for bidirectional traffic. |
| **FR-2** | Identify the user with a stable ID and a display name. | First visit mints a session ID (cookie); the user supplies a display name shown on their messages; the same browser keeps its identity across reconnects. |
| **FR-3** | Join a named room. | A client sends a `join` frame with a room name; the server adds them to that room and confirms; unknown rooms are created on first join. |
| **FR-4** | Leave a room. | A `leave` frame (or disconnect) removes the client from the room and stops delivering its messages to them. |
| **FR-5** | Send a message to a room. | A `message` frame is delivered to all other clients currently in that room. |
| **FR-6** | Receive room messages in real time. | A client in a room receives every message sent to that room, in send order, with sender name and timestamp, within a low latency budget. |
| **FR-7** | Receive recent history on join. | On joining, a client receives the last N messages of the room (from Postgres) before/with the live stream, so they have context. |
| **FR-8** | Persist messages to Postgres. | Every accepted message is durably stored (room, sender, content, timestamp) and survives a server restart. |
| **FR-9** | Show presence (join/leave). | When a user joins or leaves a room, others in the room are notified, and a client can see who is currently present. |
| **FR-10** | List available rooms. | `GET /api/rooms` returns the active rooms (and optionally member counts) for the UI to display. |
| **FR-11** | Serve the embedded web UI. | Opening the server's address in a browser loads the chat interface (HTML/CSS/JS) served from the binary. |
| **FR-12** | Shut down gracefully. | On `SIGINT`/`SIGTERM`, the server stops accepting new connections, sends a close frame to existing connections, drains pending persistence, and exits cleanly with no leaked goroutines. |
| **FR-13** | Detect dead connections (heartbeat). | The server pings clients on an interval and closes connections that fail to pong within a deadline, freeing their resources. |
| **FR-14** | Validate configuration at startup. | Missing DB URL, invalid addr, non-positive limits, etc. cause an immediate clear error before serving. |

---

## 5. Non-Functional Requirements

### 5.1 Performance & Latency
- **NFR-P1 (Delivery latency):** A message is broadcast to room members on the **hot path**, never blocked by database writes; persistence happens asynchronously on the **cold path**. (Reuses the shortener's split.)
- **NFR-P2 (Throughput):** Sustain many messages/sec across rooms bounded by CPU/network, not by internal lock contention.
- **NFR-P3 (Memory per connection):** Each connection uses a small, bounded footprint (one buffered send channel + two goroutines); idle connections cost almost nothing (Go's netpoller — Lesson 8).

### 5.2 Concurrency & Scalability
- **NFR-C1 (Connection scale):** Handle thousands of concurrent connections on a single process (goroutine-per-connection is cheap — Lesson 8).
- **NFR-C2 (Single owner of shared state):** The rooms/clients state is owned by **one hub goroutine** (actor pattern); no shared-state mutexes on the broadcast path. Passes `go test -race`.
- **NFR-C3 (Multi-server seam):** Broadcasting goes through the `MessageBus` interface; a Redis Pub/Sub implementation enables horizontal scaling **without changing the hub** (the chosen v2 design).
- **NFR-C4 (Bounded per-connection resources):** Per-client send buffers are bounded; the server never grows unbounded queues for a slow client.

### 5.3 Reliability & Correctness
- **NFR-R1 (No goroutine leaks):** Every connection's `readPump` and `writePump` terminate on disconnect/cancel; the hub unregisters the client. Goroutine count returns to baseline after clients leave (Lesson 6).
- **NFR-R2 (Slow client isolation):** A slow or stalled client **must not block** delivery to others. The hub broadcasts with a **non-blocking send**; a client whose buffer is full is dropped and disconnected. *(The defining chat-server correctness property.)*
- **NFR-R3 (Fault isolation):** A panic handling one connection must not crash the server; per-connection goroutines recover at their boundary (Lesson 5).
- **NFR-R4 (Persistence durability):** Accepted messages are not lost on graceful shutdown — the persister drains before exit.
- **NFR-R5 (Ordering):** Messages within a room are delivered and stored in a consistent order.

### 5.4 Connection Safety & Protocol
- **NFR-S1 (Single writer per connection):** Exactly one goroutine (`writePump`) ever writes to a given WebSocket; concurrent writes are forbidden (the WS library is not write-safe). *(Connection-ownership discipline.)*
- **NFR-S2 (Message size limit):** Inbound messages are capped; oversized frames are rejected (DoS protection — the WS analogue of the scraper's `io.LimitReader`).
- **NFR-S3 (Read deadlines + ping/pong):** Read deadlines plus periodic pings detect and reap dead connections (FR-13).
- **NFR-S4 (Origin checking):** The WebSocket upgrade validates the `Origin` header (cross-site WebSocket hijacking protection).

### 5.5 Usability
- **NFR-U1 (Clean UI):** The interface is presentable to a non-technical client — readable message list, room list, presence, connection status.
- **NFR-U2 (Reconnect):** The client auto-reconnects with backoff if the connection drops, restoring the experience without a manual refresh.
- **NFR-U3 (Structured logging):** Operational logs use `slog` (connections opened/closed, message rates, errors).

### 5.6 Portability & Deployment
- **NFR-D1 (Single binary):** Static binary (`CGO_ENABLED=0`) with the UI embedded; only external dependency is Postgres (and, in v2, Redis).
- **NFR-D2 (Cross-platform):** Cross-compiles via `GOOS`/`GOARCH`.
- **NFR-D3 (Containerizable):** Few-MB multi-stage image; runs alongside a Postgres container via compose for the demo.

### 5.7 Maintainability & Testability
- **NFR-M1 (Testable hub):** The hub is tested with **fake client connections** (an interface over the WS read/write), so its logic — register, broadcast, slow-client drop — is unit-testable with no real sockets.
- **NFR-M2 (Store/bus behind interfaces):** `MessageStore` and `MessageBus` are interfaces, so tests use fakes and the Redis/Postgres details stay swappable.
- **NFR-M3 (Linting/race):** Passes `gofmt`, `go vet`, `golangci-lint`, and `go test -race`.

### 5.8 Observability
- **NFR-O1 (Live metrics):** Exposes current connection count, rooms, and message rate (to logs and optionally the UI).
- **NFR-O2 (Self-profilable):** `pprof` available to inspect goroutine counts (proving NFR-R1) and CPU under load.

### 5.9 Security (intentionally limited)
- **NFR-X1 (No authentication — by design):** Identity is a session cookie (a bearer token, unverified), exactly as in the prior projects. This is **organizational, not a security boundary**; OAuth is deliberately out of scope. Stated plainly so it's an informed choice. Real auth slots in at the `session`/`UserID` seam.
- **NFR-X2 (Input bounds):** Message size caps (NFR-S2) and origin checks (NFR-S4) protect against the obvious abuse vectors even without auth.

---

## 6. Key Design Decisions & Tradeoffs

| Decision | Choice | Rationale | Alternative (rejected) |
|----------|--------|-----------|------------------------|
| Transport | **WebSockets** | Bidirectional, persistent — clients send *and* receive | SSE (one-way; used in prior projects, insufficient here) |
| Shared state | **Hub actor goroutine** owns rooms/clients; communicates via channels | No mutexes on the broadcast path; race-free by construction (same as scraper coordinator) | Mutex-guarded shared maps (more lock surface, easier to get wrong) |
| Per-connection | **Two goroutines** (read + write pumps) + buffered `send` channel | Single writer per connection (WS not write-safe); clean lifecycle | One goroutine doing both (can't block on read and write simultaneously) |
| Slow clients | **Non-blocking broadcast**; drop+disconnect on full buffer | One slow client can't stall everyone else (NFR-R2) | Blocking send (one slow client freezes the room) |
| Persistence | **Async worker** (hot path delivers, cold path persists) | Delivery latency independent of DB latency (NFR-P1) | Persist-then-broadcast (DB latency on every message) |
| Scaling | **`MessageBus` seam**; LocalBus v1, RedisBus v2 | Cheap seam now, drop-in multi-server later, no hub rewrite | Build Redis now (over-engineering, undemoable) / no seam (costly refactor) |
| History | **Postgres**, last N on join | Durable, survives restart, gives context (FR-7) | In-memory only (lost on restart) |
| Rooms | **Named public rooms** | Simple, demoable, the common case | DMs/private (deferred to v2) |

---

## 7. Dependencies (each justified)

| Dependency | Purpose | Notes |
|------------|---------|-------|
| `github.com/coder/websocket` (or `gorilla/websocket`) | WebSocket upgrade + framing | The core of the project; `coder/websocket` is modern and context-friendly |
| `github.com/jackc/pgx/v5` | Postgres driver + pool | Higher-performance than `lib/pq`; context-aware. (`database/sql` + `pgx` stdlib mode also fine) |
| `github.com/google/uuid` | Session/message IDs | Same as prior projects |
| `golang.org/x/sync/errgroup` | Coordinated goroutine shutdown | Server + persister lifecycle (L6) |
| `github.com/redis/go-redis/v9` | (v2 only) Redis Pub/Sub backplane | Documented seam; **not** a v1 dependency |

Everything else — `net/http`, `encoding/json`, `log/slog`, `embed`, `context`,
`sync` — is standard library.

---

## 8. Data Flow

```
CONNECT
  browser ── WS upgrade ──► web.server (origin check, session → UserID)
            └─► new Client{conn, send chan} ──► readPump + writePump goroutines start
                                            └─► Client registers with Hub

JOIN ROOM
  client ─{join,room}─► readPump ─► hub.inbound
        hub: add client to room.members
             store.RecentByRoom(room) ──► send history frames to THIS client (send chan)
             broadcast presence "X joined" to room

SEND MESSAGE
  client ─{message,room,text}─► readPump ─► hub.inbound
        hub (HOT PATH):  for each member m in room:
                            select { case m.send <- frame: ok
                                     default: drop+close m }   ◄── non-blocking (NFR-R2)
        hub (COLD PATH): persist.ch <- message ──► async worker ──► store.SaveMessage ──► Postgres

RECEIVE
  writePump: for frame := range client.send { conn.Write(frame) } ──► browser renders

DISCONNECT
  conn closes ─► readPump errors ─► hub.unregister ─► remove from rooms ─► close(send)
              ─► writePump's range ends ─► goroutine exits (NFR-R1) ─► broadcast "X left"
```

The `MessageBus` seam sits at the HOT PATH: in v1 the hub delivers to local members
directly; in v2 the hub `bus.Publish`es and a `bus.Subscribe` loop (fed by Redis,
across all servers) is what triggers local delivery — same hub, swapped bus.

---

## 9. Build Order (bottom-up; always runnable)

1. `config` — `Config` + `Validate`.
2. `message` — protocol types (`Envelope`, `Message`), JSON-tagged.
3. `store` — `MessageStore` interface + Postgres impl + `migrations/0001_init.sql` (test with `t.TempDir`/test DB or a fake).
4. `bus` — `MessageBus` interface + `LocalBus`.
5. `hub` — `room`, `client` (with a fake conn interface), `hub` actor + broadcast + slow-client drop (+ race test). **The big one.**
6. `persist` — async worker draining to the store.
7. `web` — HTTP server, `/ws` upgrade, session, REST routes, embedded UI.
8. `main` — wire everything + graceful shutdown. **✅ Two-browser live demo.**
9. UI polish (presence, reconnect, connection status) + README + Dockerfile + compose (Postgres).

Working chat at step 8; polished demo at step 9. Postgres is the only infra needed
for v1 (run it via Docker Compose for the demo).

---

## 10. Out of Scope (v1)

Deferred, each a natural v2 talking point: **OAuth/real authentication** (the session
seam is ready for it), the **Redis Pub/Sub multi-server backplane** (the `MessageBus`
seam is ready for it), **private/1-on-1 DMs**, **typing indicators & read receipts**,
**file/image uploads**, **message editing/deletion**, **end-to-end encryption**, and
**moderation/rate-limiting per user**. Naming these keeps v1 shippable and gives you
a credible roadmap conversation with clients.
```

