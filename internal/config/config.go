// Package config holds the server's runtime configuration and validates it at
// startup so misconfiguration fails fast and loudly (FR-14, CODING-STANDARDS §10).
package config

import (
	"errors"
	"fmt"
	"time"
)

// Default configuration values. They are real defaults, not magic numbers scattered
// through the code — every limit and address is config (CODING-STANDARDS §10).
const (
	defaultAddr           = ":8080"
	defaultMaxMessageSize = 4 << 10 // 4 KiB cap on inbound frames (NFR-S2).
	defaultSendBuffer     = 16      // per-client send-channel depth (NFR-C4).
	defaultPingInterval   = 30 * time.Second
	defaultHistoryLimit   = 50 // messages returned to a client on join (FR-7).
	defaultMaxRooms       = 256
	defaultSessionTTL     = 7 * 24 * time.Hour // how long a login session lasts.
)

// Config is the fully resolved configuration for one server process. It is built
// from flags/env in main, then checked with Validate before anything starts.
type Config struct {
	// Addr is the host:port the HTTP/WebSocket server listens on.
	Addr string
	// DBURL is the Postgres connection string for message persistence.
	DBURL string
	// MaxMessageSize bounds an inbound WebSocket frame, in bytes (NFR-S2).
	MaxMessageSize int64
	// SendBuffer is the depth of each client's buffered send channel (NFR-C4).
	SendBuffer int
	// PingInterval is how often the server pings clients to detect dead ones (FR-13).
	PingInterval time.Duration
	// HistoryLimit is how many recent messages a joining client receives (FR-7).
	HistoryLimit int
	// MaxRooms caps the number of concurrently active rooms.
	MaxRooms int
	// AllowedOrigins lists Origin header values accepted on the WS upgrade (NFR-S4).
	// Empty means same-origin only.
	AllowedOrigins []string
	// SessionTTL is how long a login session remains valid.
	SessionTTL time.Duration
	// SecureCookies marks the session cookie Secure (HTTPS-only). Leave false for
	// local HTTP development; set true behind TLS in production.
	SecureCookies bool
	// DebugAddr is the address for the pprof debug server (NFR-O2). Empty disables
	// it; set a localhost address like "127.0.0.1:6060" to enable profiling.
	DebugAddr string
}

// Default returns a Config populated with sensible defaults. Callers override fields
// from flags/env before calling Validate.
func Default() Config {
	return Config{
		Addr:           defaultAddr,
		MaxMessageSize: defaultMaxMessageSize,
		SendBuffer:     defaultSendBuffer,
		PingInterval:   defaultPingInterval,
		HistoryLimit:   defaultHistoryLimit,
		MaxRooms:       defaultMaxRooms,
		SessionTTL:     defaultSessionTTL,
	}
}

// ErrMissingDBURL indicates the required Postgres connection string was not provided.
var ErrMissingDBURL = errors.New("config: database URL is required")

// Validate reports the first reason the configuration is unusable, or nil if the
// config is safe to run with. It is called once at startup (fail-fast, FR-14).
func (c Config) Validate() error {
	if c.Addr == "" {
		return errors.New("config: listen address is required")
	}
	if c.DBURL == "" {
		return ErrMissingDBURL
	}
	if c.MaxMessageSize <= 0 {
		return fmt.Errorf("config: max message size must be positive, got %d", c.MaxMessageSize)
	}
	if c.SendBuffer <= 0 {
		return fmt.Errorf("config: send buffer must be positive, got %d", c.SendBuffer)
	}
	if c.PingInterval <= 0 {
		return fmt.Errorf("config: ping interval must be positive, got %s", c.PingInterval)
	}
	if c.HistoryLimit < 0 {
		return fmt.Errorf("config: history limit must not be negative, got %d", c.HistoryLimit)
	}
	if c.MaxRooms <= 0 {
		return fmt.Errorf("config: max rooms must be positive, got %d", c.MaxRooms)
	}
	if c.SessionTTL <= 0 {
		return fmt.Errorf("config: session TTL must be positive, got %s", c.SessionTTL)
	}
	return nil
}
