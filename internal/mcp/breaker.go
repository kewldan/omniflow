package mcp

import (
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen reports a server Omniflow has stopped calling.
//
// It is deliberately a distinct error rather than the underlying failure,
// because "we are not calling this right now" and "this call failed" lead to
// different operator actions and different retry behaviour.
var ErrCircuitOpen = errors.New("mcp server is temporarily unavailable")

// Breaker stops calling a server that keeps failing.
//
// The point is not to protect the server; it is that an operator waiting on a
// support ticket should get "this connection is down" in milliseconds rather
// than a twenty-second timeout per attempt. A breaker that only saved the
// remote party would not be worth the state.
type Breaker struct {
	// FailureThreshold is how many consecutive failures open the circuit.
	// Consecutive rather than a rate, because a rate needs a window and a window
	// needs tuning, and an owner should not have to tune this.
	FailureThreshold int
	// Cooldown is how long the circuit stays open before one probe is allowed.
	Cooldown time.Duration

	clock func() time.Time

	mutex    sync.Mutex
	states   map[string]*breakerState
	initOnce sync.Once
}

type breakerState struct {
	failures int
	openedAt time.Time
	// probing marks the single half-open call. One at a time, so a recovering
	// server does not get the whole backlog at once and immediately fall over.
	probing bool
}

// Breaker defaults. Three failures is enough to distinguish a blip from an
// outage, and thirty seconds is short enough that a recovered server comes back
// without an operator restarting anything.
const (
	DefaultFailureThreshold = 3
	DefaultCooldown         = 30 * time.Second
)

// NewBreaker builds a breaker with the defaults applied.
func NewBreaker() *Breaker {
	return &Breaker{
		FailureThreshold: DefaultFailureThreshold,
		Cooldown:         DefaultCooldown,
		clock:            time.Now,
		states:           map[string]*breakerState{},
	}
}

func (breaker *Breaker) state(slug string) *breakerState {
	breaker.initOnce.Do(func() {
		if breaker.states == nil {
			breaker.states = map[string]*breakerState{}
		}
		if breaker.clock == nil {
			breaker.clock = time.Now
		}
		if breaker.FailureThreshold <= 0 {
			breaker.FailureThreshold = DefaultFailureThreshold
		}
		if breaker.Cooldown <= 0 {
			breaker.Cooldown = DefaultCooldown
		}
	})
	current, known := breaker.states[slug]
	if !known {
		current = &breakerState{}
		breaker.states[slug] = current
	}
	return current
}

// Allow reports whether a call to a server may proceed.
func (breaker *Breaker) Allow(slug string) error {
	breaker.mutex.Lock()
	defer breaker.mutex.Unlock()

	state := breaker.state(slug)
	if state.failures < breaker.FailureThreshold {
		return nil
	}
	if breaker.clock().Sub(state.openedAt) < breaker.Cooldown {
		return ErrCircuitOpen
	}
	if state.probing {
		return ErrCircuitOpen
	}
	state.probing = true
	return nil
}

// Succeeded closes the circuit.
func (breaker *Breaker) Succeeded(slug string) {
	breaker.mutex.Lock()
	defer breaker.mutex.Unlock()
	state := breaker.state(slug)
	state.failures, state.probing = 0, false
}

// Failed records a failure and opens the circuit at the threshold.
//
// Only transport and protocol failures belong here. A tool that ran and
// returned an error is a working connection giving an answer, and counting it
// would take a server offline for correctly refusing a bad argument.
func (breaker *Breaker) Failed(slug string) {
	breaker.mutex.Lock()
	defer breaker.mutex.Unlock()
	state := breaker.state(slug)
	state.failures++
	state.probing = false
	if state.failures >= breaker.FailureThreshold {
		state.openedAt = breaker.clock()
	}
}

// Open reports whether the circuit is currently refusing calls. It is what the
// health display reads, so an owner sees "unavailable" rather than a server
// that merely looks slow.
func (breaker *Breaker) Open(slug string) bool {
	return breaker.Allow(slug) != nil
}
