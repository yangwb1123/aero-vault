package thumbnail

import (
	"context"
	"errors"
)

var errCoalescedLeaderPanic = errors.New("thumbnail: coalesced leader panicked")

type cacheFlight struct {
	done    chan struct{}
	result  CachedGenerationResult
	err     error
	joiners int
}

// maxPendingFlights bounds distinct entries in one Cache's in-flight registry.
// It deliberately shares the decode capacity's small fixed bound: leaders
// waiting for admission, active leaders, and completed flights awaiting map
// removal all count, while followers do not. A full registry does not evict or
// cancel existing work; a new key bypasses coalescing and uses direct
// generation. This does not bound followers or the decode-admission queue.
const maxPendingFlights = maxConcurrentDecodes

func beginFlight(c *Cache, key CacheKey) (*cacheFlight, bool) {
	if c == nil || c.disabled || !key.Identity.Complete() {
		return nil, false
	}
	c.flightMu.Lock()
	defer c.flightMu.Unlock()
	// Existing-key lookup must precede the capacity check so a full registry
	// still preserves same-key coalescing.
	if flight, ok := c.flights[key]; ok {
		flight.joiners++
		return flight, false
	}
	if len(c.flights) >= maxPendingFlights {
		return nil, false
	}
	if c.flights == nil {
		c.flights = make(map[CacheKey]*cacheFlight)
	}
	flight := &cacheFlight{done: make(chan struct{})}
	c.flights[key] = flight
	return flight, true
}

func finishFlight(c *Cache, key CacheKey, flight *cacheFlight, result CachedGenerationResult, err error) {
	flight.result = result
	flight.err = err
	close(flight.done)
	c.flightMu.Lock()
	if c.flights[key] == flight {
		delete(c.flights, key)
	}
	c.flightMu.Unlock()
}

func waitFlight(ctx context.Context, flight *cacheFlight) ([]byte, bool, error) {
	result, err := waitFlightResult(ctx, flight)
	return result.Image, result.FromCache, err
}

func waitFlightResult(ctx context.Context, flight *cacheFlight) (CachedGenerationResult, error) {
	select {
	case <-flight.done:
		return joinFlightResult(flight)
	case <-ctx.Done():
		select {
		case <-flight.done:
			return joinFlightResult(flight)
		default:
			return CachedGenerationResult{}, ctx.Err()
		}
	}
}

func joinFlightResult(flight *cacheFlight) (CachedGenerationResult, error) {
	result := flight.result
	if flight.err == nil {
		result.FromCache = true
	}
	return result, flight.err
}
