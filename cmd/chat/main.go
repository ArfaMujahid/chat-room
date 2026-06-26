// Command chat is the entry point and composition root for the chat server. It is
// deliberately thin: parse flags, validate config, wire the store/hub/persister/
// server together, then run with graceful shutdown. No business logic lives here
// (CODING-STANDARDS §7, architecture §3).
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ArfaMujahid/chat-room/internal/auth"
	"github.com/ArfaMujahid/chat-room/internal/config"
	"github.com/ArfaMujahid/chat-room/internal/hub"
	"github.com/ArfaMujahid/chat-room/internal/persist"
	"github.com/ArfaMujahid/chat-room/internal/store"
	"github.com/ArfaMujahid/chat-room/internal/web"
)

// persistQueueDepth bounds the cold-path queue between the hub and the persister.
const persistQueueDepth = 1024

// shutdownGrace is how long graceful shutdown waits for in-flight work before giving
// up (FR-12).
const shutdownGrace = 10 * time.Second

// main wires the server and runs it, exiting non-zero on a startup or run error.
func main() {
	if err := run(); err != nil {
		slog.Error("chat: fatal", "err", err)
		os.Exit(1)
	}
}

// run builds and runs the whole server, returning the first error that should stop
// the process. Splitting it out of main lets defers run and keeps main trivial.
func run() error {
	cfg := parseFlags()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	if err := cfg.Validate(); err != nil {
		return err
	}

	// signal.NotifyContext cancels ctx on SIGINT/SIGTERM, the trigger for graceful
	// shutdown (FR-12).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.NewPostgres(ctx, cfg.DBURL)
	if err != nil {
		return err
	}
	// Closed last (after g.Wait below), so the persister can still drain to it during
	// shutdown (NFR-R4).
	defer func() {
		if cerr := st.Close(); cerr != nil {
			logger.Error("chat: closing store", "err", cerr)
		}
	}()

	persister := persist.New(st, persistQueueDepth, logger)
	h := hub.New(st, persister.Inbox(), cfg.HistoryLimit, logger)
	// Auth shares the store's connection pool and the schema it migrated.
	authSvc := auth.NewService(auth.NewPostgres(st.Pool()), cfg.SessionTTL)

	// errgroup ties the components together: if any returns (signal, serve error),
	// gctx is cancelled and the rest wind down (CODING-STANDARDS §4).
	g, gctx := errgroup.WithContext(ctx)

	srv, err := web.New(gctx, cfg, h, authSvc, logger)
	if err != nil {
		return err
	}

	// Long-running components; each returns when gctx is cancelled (NFR-R1). The
	// persister drains its queue on cancel before returning (NFR-R4).
	g.Go(func() error { h.Run(gctx); return nil })
	g.Go(func() error { persister.Run(gctx); return nil })
	g.Go(func() error { return srv.Serve() })

	// Graceful shutdown: stop accepting connections and drain in-flight HTTP work
	// within the grace period once shutdown begins (FR-12).
	g.Go(func() error {
		<-gctx.Done()
		logger.Info("chat: shutdown signal received, draining")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	})

	return g.Wait()
}

// parseFlags builds a Config from defaults overridden by command-line flags. Every
// limit is a flag with a default — nothing is hardcoded (CODING-STANDARDS §10).
func parseFlags() config.Config {
	cfg := config.Default()
	flag.StringVar(&cfg.Addr, "addr", cfg.Addr, "host:port to listen on")
	flag.StringVar(&cfg.DBURL, "db-url", cfg.DBURL, "Postgres connection string (required)")
	flag.Int64Var(&cfg.MaxMessageSize, "max-message-size", cfg.MaxMessageSize, "max inbound frame size in bytes")
	flag.IntVar(&cfg.SendBuffer, "send-buffer", cfg.SendBuffer, "per-client send channel depth")
	flag.DurationVar(&cfg.PingInterval, "ping-interval", cfg.PingInterval, "interval between heartbeat pings")
	flag.IntVar(&cfg.HistoryLimit, "history-limit", cfg.HistoryLimit, "recent messages sent to a client on join")
	flag.IntVar(&cfg.MaxRooms, "max-rooms", cfg.MaxRooms, "maximum concurrently active rooms")
	flag.DurationVar(&cfg.SessionTTL, "session-ttl", cfg.SessionTTL, "how long a login session lasts")
	flag.BoolVar(&cfg.SecureCookies, "secure-cookies", cfg.SecureCookies, "mark the session cookie Secure (HTTPS only)")
	flag.Parse()
	return cfg
}
