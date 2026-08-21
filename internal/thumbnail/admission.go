package thumbnail

import (
	"context"
	"sync"
)

// DecodeAdmission adds an optional per-tenant ceiling to the package-global
// decode semaphore. It is deliberately separate from the HTTP concurrency
// limiter: the latter can admit several requests before a thumbnail opens its
// object stream, while this gate owns the allocation-bearing decode slot.
//
// A tenant slot is acquired before the global slot. This ordering matters: a
// tenant waiting for its own ceiling must not hold one of the four global
// slots and thereby starve every other tenant. A nil admission, or an
// admission configured with a non-positive limit, preserves the historical
// global-only path.
type DecodeAdmission struct {
	perTenant int

	mu     sync.Mutex
	states map[string]*tenantDecodeState
}

type tenantDecodeState struct {
	active  int
	waiters int
	wake    chan struct{}
}

// NewDecodeAdmission returns an opt-in per-tenant decode admission gate. A
// limit of zero or less disables the extra gate and returns nil. Values above
// the global decode capacity are clamped because a larger tenant allowance
// cannot increase useful concurrency and would only mislead operators.
func NewDecodeAdmission(perTenant int) *DecodeAdmission {
	if perTenant <= 0 {
		return nil
	}
	if perTenant > maxConcurrentDecodes {
		perTenant = maxConcurrentDecodes
	}
	return &DecodeAdmission{
		perTenant: perTenant,
		states:    make(map[string]*tenantDecodeState),
	}
}

// NewPerTenantDecodeAdmission is a descriptive alias for callers that want
// to make the fairness boundary explicit at the composition root.
func NewPerTenantDecodeAdmission(perTenant int) *DecodeAdmission {
	return NewDecodeAdmission(perTenant)
}

// Acquire reserves one tenant slot and one global decode slot. The returned
// release function is safe to call more than once, which keeps adapter error
// paths from turning a double-defer into a semaphore imbalance.
func (a *DecodeAdmission) Acquire(ctx context.Context, tenant string) (func(), error) {
	if a == nil || a.perTenant <= 0 {
		if err := acquireDecodeSlotContext(ctx); err != nil {
			return nil, err
		}
		return onceRelease(releaseDecodeSlot), nil
	}
	if err := a.acquireTenant(ctx, tenant); err != nil {
		return nil, err
	}
	if err := acquireDecodeSlotContext(ctx); err != nil {
		a.releaseTenant(tenant)
		return nil, err
	}
	return onceRelease(func() {
		releaseDecodeSlot()
		a.releaseTenant(tenant)
	}), nil
}

func (a *DecodeAdmission) acquireTenant(ctx context.Context, tenant string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.mu.Lock()
		state := a.stateLocked(tenant)
		if state.active < a.perTenant {
			state.active++
			if err := ctx.Err(); err != nil {
				state.active--
				a.cleanupLocked(tenant, state)
				a.mu.Unlock()
				return err
			}
			a.mu.Unlock()
			return nil
		}
		state.waiters++
		wake := state.wake
		a.mu.Unlock()

		select {
		case <-wake:
			a.mu.Lock()
			state.waiters--
			a.cleanupLocked(tenant, state)
			a.mu.Unlock()
		case <-ctx.Done():
			a.mu.Lock()
			state.waiters--
			a.cleanupLocked(tenant, state)
			a.mu.Unlock()
			return ctx.Err()
		}
	}
}

func (a *DecodeAdmission) releaseTenant(tenant string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.states[tenant]
	if state == nil || state.active == 0 {
		return
	}
	state.active--
	if state.waiters > 0 {
		close(state.wake)
		state.wake = make(chan struct{})
	}
	a.cleanupLocked(tenant, state)
}

func (a *DecodeAdmission) stateLocked(tenant string) *tenantDecodeState {
	state := a.states[tenant]
	if state == nil {
		state = &tenantDecodeState{wake: make(chan struct{})}
		a.states[tenant] = state
	}
	return state
}

func (a *DecodeAdmission) cleanupLocked(tenant string, state *tenantDecodeState) {
	if state.active == 0 && state.waiters == 0 {
		delete(a.states, tenant)
	}
}

func onceRelease(release func()) func() {
	var once sync.Once
	return func() { once.Do(release) }
}
