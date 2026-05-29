// Package events: PostgresTransport bridges the otherwise in-process event Bus
// across replicas using Postgres LISTEN/NOTIFY.
//
// # Architecture
//
// The local Bus (bus.go) only fans events out to subscribers within a single
// process. In a multi-replica deployment, an event published on replica A is
// invisible to subscribers (Pipeline workers, webhook fanout) on replica B.
// PostgresTransport closes that gap: Publish broadcasts an event to all
// replicas via pg_notify, and Run (one per replica) LISTENs and re-delivers
// received events into the local Bus via the supplied deliver callback.
//
// Typical wiring (done by the parent, NOT here):
//
//	tr := events.NewPostgresTransport(dsn, "")
//	go tr.Run(ctx, func(e repository.Event) { bus.Publish(ctx, e) }) // or a re-broadcast hook
//	// and on local publish, also call tr.Publish(ctx, e)
//
// Status: OPT-IN. REQUIRES Postgres. This file is NOT exercised by CI tests —
// the test harness has no Postgres, so only the structural/encoding helpers
// below are unit-tested. The actual LISTEN/NOTIFY round-trip is UNVERIFIED.
//
// Caveats:
//   - Delivery is at-least-once and best-effort: NOTIFY fires only on commit of
//     the issuing transaction and is not persisted, so notifications sent while
//     a replica is disconnected are lost. The durable event table (see
//     repository.InsertEvent / NextUnconsumedEvents) remains the source of
//     truth; this transport is a low-latency wakeup, not a guaranteed log.
//   - Postgres caps a NOTIFY payload at 8000 bytes. Events whose JSON exceeds
//     that will be rejected by the server. For large events, callers should
//     carry only the event id in the payload and re-fetch the full row from the
//     durable event table on the receiving side.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/jackc/pgx/v5"
)

// DefaultChannel is used when NewPostgresTransport is given an empty channel.
const DefaultChannel = "aero_vault_events"

// PostgresTransport carries repository.Event values between replicas over a
// Postgres LISTEN/NOTIFY channel. It stores configuration only; no connection
// is opened until Publish or Run is called.
type PostgresTransport struct {
	dsn     string
	channel string
}

// NewPostgresTransport returns a transport configured for dsn and channel.
// If channel is empty, DefaultChannel is used. No connection is made here.
func NewPostgresTransport(dsn, channel string) *PostgresTransport {
	if channel == "" {
		channel = DefaultChannel
	}
	return &PostgresTransport{dsn: dsn, channel: channel}
}

// Channel reports the LISTEN/NOTIFY channel this transport uses.
func (t *PostgresTransport) Channel() string { return t.channel }

// Publish marshals e to JSON and emits it on the configured channel via
// pg_notify, using a short-lived connection. The 8000-byte NOTIFY payload
// limit applies (see package doc).
func (t *PostgresTransport) Publish(ctx context.Context, e repository.Event) error {
	payload, err := encodeEvent(e)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}

	conn, err := pgx.Connect(ctx, t.dsn)
	if err != nil {
		return fmt.Errorf("postgres transport connect: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	// pg_notify(channel, payload) is the function form of NOTIFY and accepts
	// bind parameters, so the channel/payload need not be string-escaped.
	if _, err := conn.Exec(ctx, "SELECT pg_notify($1, $2)", t.channel, string(payload)); err != nil {
		return fmt.Errorf("postgres transport notify: %w", err)
	}
	return nil
}

// Run opens a dedicated pgx connection, issues LISTEN on the configured
// channel, and loops on WaitForNotification. Each received payload is decoded
// into a repository.Event and passed to deliver (typically a re-broadcast into
// the local Bus). Run reconnects with a bounded backoff on connection errors
// and returns when ctx is cancelled. A native pgx connection is required —
// database/sql does not surface asynchronous notifications.
func (t *PostgresTransport) Run(ctx context.Context, deliver func(repository.Event)) error {
	const (
		minBackoff = 250 * time.Millisecond
		maxBackoff = 30 * time.Second
	)
	backoff := minBackoff

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		err := t.listenLoop(ctx, deliver)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			// listenLoop only returns nil if ctx ended, handled above.
			continue
		}

		// Connection-level failure: back off, then reconnect.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// listenLoop holds one connection: it LISTENs and dispatches notifications
// until ctx is done (returns ctx error) or a connection error occurs (returns
// that error so Run can reconnect).
func (t *PostgresTransport) listenLoop(ctx context.Context, deliver func(repository.Event)) error {
	conn, err := pgx.Connect(ctx, t.dsn)
	if err != nil {
		return fmt.Errorf("postgres transport connect: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	// LISTEN target is an identifier and cannot be a bind parameter, so it is
	// interpolated. t.channel originates from trusted config, not user input.
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{t.channel}.Sanitize()); err != nil {
		return fmt.Errorf("postgres transport listen: %w", err)
	}

	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("postgres transport wait: %w", err)
		}
		e, err := decodeEvent([]byte(n.Payload))
		if err != nil {
			// A malformed payload must not kill the loop; skip it. The durable
			// event table remains authoritative.
			continue
		}
		deliver(e)
	}
}

// encodeEvent marshals an event to its NOTIFY wire form (JSON).
func encodeEvent(e repository.Event) ([]byte, error) {
	return json.Marshal(e)
}

// decodeEvent is the inverse of encodeEvent.
func decodeEvent(b []byte) (repository.Event, error) {
	var e repository.Event
	if len(b) == 0 {
		return e, errors.New("empty event payload")
	}
	if err := json.Unmarshal(b, &e); err != nil {
		return e, err
	}
	return e, nil
}
