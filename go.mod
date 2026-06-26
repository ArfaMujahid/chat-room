module github.com/ArfaMujahid/chat-room

go 1.26

// Dependencies are added as each package is implemented — see chat-architecture.md
// §7 for the full justified list (websocket, uuid, errgroup, go-redis still pending).

require (
	github.com/coder/websocket v1.8.15
	github.com/jackc/pgx/v5 v5.10.0
	golang.org/x/sync v0.17.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/text v0.29.0 // indirect
)
