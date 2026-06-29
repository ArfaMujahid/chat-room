// Command chat is the entry point and composition root for the chat server. It is
// deliberately thin: parse flags, validate config, wire the store/hub/persister/
// server together, then run with graceful shutdown. No business logic lives here
// (CODING-STANDARDS §7, architecture §3).
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	_ "net/http/pprof" // registers pprof handlers on http.DefaultServeMux
	"os"
	"os/signal"
	"strings"
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

	msgStore, authStore, closeStore, err := openStores(ctx, cfg, logger)
	if err != nil {
		return err
	}
	// Closed last (after g.Wait below), so the persister can still drain to it during
	// shutdown (NFR-R4).
	defer func() {
		if cerr := closeStore(); cerr != nil {
			logger.Error("chat: closing store", "err", cerr)
		}
	}()

	persister := persist.New(msgStore, persistQueueDepth, logger)
	h := hub.New(msgStore, persister.Inbox(), cfg.HistoryLimit, logger)
	authSvc := auth.NewService(authStore, cfg.SessionTTL)

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

	// Optional pprof server for profiling goroutines/CPU under load (NFR-O2).
	if cfg.DebugAddr != "" {
		startDebugServer(gctx, g, cfg.DebugAddr, logger)
	}

	return g.Wait()
}

// openStores selects the database from the configured DSN and returns the message
// store, auth store, and a close function. A postgres:// URL uses Postgres (one
// shared pool); anything else uses the embedded SQLite store (one shared handle), so
// the binary runs with no external services.
func openStores(ctx context.Context, cfg config.Config, logger *slog.Logger) (store.MessageStore, auth.Store, func() error, error) {
	if strings.HasPrefix(cfg.DBURL, "postgres://") || strings.HasPrefix(cfg.DBURL, "postgresql://") {
		pg, err := store.NewPostgres(ctx, cfg.DBURL)
		if err != nil {
			return nil, nil, nil, err
		}
		logger.Info("chat: using postgres store")
		return pg, auth.NewPostgres(pg.Pool()), pg.Close, nil
	}
	path := strings.TrimPrefix(cfg.DBURL, "sqlite:")
	if path == "" {
		path = "chat.db"
	}
	sq, err := store.NewSQLite(ctx, path)
	if err != nil {
		return nil, nil, nil, err
	}
	logger.Info("chat: using sqlite store", "path", path)
	return sq, auth.NewSQLite(sq.DB()), sq.Close, nil
}

// startDebugServer runs a pprof HTTP server on addr until ctx is cancelled. The pprof
// handlers are registered on http.DefaultServeMux by the net/http/pprof import; the
// chat server uses its own mux, so profiling never leaks onto the public port. Bind
// it to localhost in practice — it is unauthenticated.
func startDebugServer(ctx context.Context, g *errgroup.Group, addr string, logger *slog.Logger) {
	dbg := &http.Server{Addr: addr, Handler: http.DefaultServeMux, ReadHeaderTimeout: 10 * time.Second}
	g.Go(func() error {
		logger.Info("chat: pprof debug server listening", "addr", addr)
		if err := dbg.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		return dbg.Shutdown(shutdownCtx)
	})
}

// parseFlags builds a Config from defaults overridden by command-line flags. Every
// limit is a flag with a default — nothing is hardcoded (CODING-STANDARDS §10).
func parseFlags() config.Config {
	cfg := config.Default()
	flag.StringVar(&cfg.Addr, "addr", cfg.Addr, "host:port to listen on")
	flag.StringVar(&cfg.DBURL, "db-url", cfg.DBURL, "postgres:// URL for Postgres, or a SQLite file path (default: chat.db)")
	flag.Int64Var(&cfg.MaxMessageSize, "max-message-size", cfg.MaxMessageSize, "max inbound frame size in bytes")
	flag.IntVar(&cfg.SendBuffer, "send-buffer", cfg.SendBuffer, "per-client send channel depth")
	flag.DurationVar(&cfg.PingInterval, "ping-interval", cfg.PingInterval, "interval between heartbeat pings")
	flag.IntVar(&cfg.HistoryLimit, "history-limit", cfg.HistoryLimit, "recent messages sent to a client on join")
	flag.IntVar(&cfg.MaxRooms, "max-rooms", cfg.MaxRooms, "maximum concurrently active rooms")
	flag.DurationVar(&cfg.SessionTTL, "session-ttl", cfg.SessionTTL, "how long a login session lasts")
	flag.BoolVar(&cfg.SecureCookies, "secure-cookies", cfg.SecureCookies, "mark the session cookie Secure (HTTPS only)")
	flag.StringVar(&cfg.DebugAddr, "debug-addr", cfg.DebugAddr, "address for the pprof debug server, e.g. 127.0.0.1:6060 (empty disables it)")
	flag.Parse()
	return cfg
}
