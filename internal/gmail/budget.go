package gmail

import (
	"context"
	"sync"
	"time"
)

// Gmail allows roughly 250 quota units per second per user. Every method has
// a unit cost (messages.get is 5), so the limiter budgets units, not requests.
const (
	perUserUnitsPerSecond = 250.0
	minRate               = 5.0 // never throttle below one messages.get per second
	probeEvery            = 5 * time.Minute
)

// Budget is a token bucket measured in Gmail quota units, shared by every
// request of one account. It adapts to the server (AIMD): the rate is halved
// when Gmail answers with a rate-limit error and creeps back up while
// requests succeed.
//
// Waiters are served in order: Wait takes its units immediately and sleeps
// for the debt, so a burst of workers cannot overshoot the budget.
type Budget struct {
	mu       sync.Mutex
	max      float64 // configured ceiling, units per second
	rate     float64 // current rate, units per second
	burst    float64
	tokens   float64
	last     time.Time
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error
	throttle time.Duration // total time spent waiting, for metrics
	pausedTo time.Time     // nobody sends before this (after a rate-limit response)

	// Learned pacing: the rate at which Gmail pushed back becomes a ceiling a
	// little below it, and the ceiling is probed upwards while all is quiet.
	ceiling   float64   // never above max
	lastEvent time.Time // last rate-limit hit or probe
	hits      int
	paused    time.Duration // total pause time requested after rate limits
}

// NewBudget returns a budget of fraction (0, 1] of Gmail's per-user quota.
func NewBudget(fraction float64) *Budget { return NewLearnedBudget(fraction, 0) }

// NewLearnedBudget is NewBudget that starts below fraction when an earlier run
// learned that the account tolerates less: it begins at 110 % of what that run
// ended at (never above fraction) and probes upwards from there.
func NewLearnedBudget(fraction, learned float64) *Budget {
	if fraction <= 0 || fraction > 1 {
		fraction = 0.6
	}
	max := perUserUnitsPerSecond * fraction
	start := max
	if learned > 0 && learned*1.1 < fraction {
		start = maxFloat(perUserUnitsPerSecond*learned*1.1, minRate)
	}
	b := &Budget{max: max, rate: start, ceiling: start, burst: maxFloat(start, 10), tokens: maxFloat(start, 10), now: time.Now, sleep: sleepCtx}
	b.last = b.now()
	b.lastEvent = b.last
	return b
}

// Wait blocks until cost units are available or ctx ends. On cancellation the
// units are given back.
func (b *Budget) Wait(ctx context.Context, cost int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c := float64(cost)
	b.mu.Lock()
	b.refill()
	b.tokens -= c
	var delay time.Duration
	if b.tokens < 0 {
		delay = time.Duration(-b.tokens / b.rate * float64(time.Second))
	}
	if p := b.pausedTo.Sub(b.now()); p > delay {
		delay = p
	}
	b.mu.Unlock()
	if delay <= 0 {
		return nil
	}
	if err := b.sleep(ctx, delay); err != nil {
		b.mu.Lock()
		b.tokens += c
		b.mu.Unlock()
		return err
	}
	b.mu.Lock()
	b.throttle += delay
	b.mu.Unlock()
	return nil
}

// Slow reacts to a rate-limit response. The rate at which Gmail pushed back
// is remembered: the ceiling drops to 80 % of it and the rate restarts at 75 %
// of that, then creeps back up to the ceiling. The ceiling itself is probed
// upwards while nothing goes wrong (see refill), so a long run settles near
// what the account really tolerates instead of oscillating around the
// configured maximum.
func (b *Budget) Slow() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	b.hits++
	b.ceiling = maxFloat(minFloat(b.ceiling, b.rate*0.8), minRate)
	b.rate = maxFloat(b.ceiling*0.75, minRate)
	b.lastEvent = b.now()
	if b.tokens > 0 {
		b.tokens = 0
	}
}

// Stats reports how often Gmail pushed back, the total pause requested, and
// the current rate and learned ceiling in quota units per second.
func (b *Budget) Stats() (hits int, paused time.Duration, rate, ceiling float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	return b.hits, b.paused, b.rate, b.ceiling
}

// Pause makes every caller wait until d from now. A rate-limit response is
// about the whole account, so all workers stop together instead of each one
// finding out separately.
func (b *Budget) Pause(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.paused += d
	if until := b.now().Add(d); until.After(b.pausedTo) {
		b.pausedTo = until
	}
}

// Rate returns the current rate in units per second.
func (b *Budget) Rate() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	return b.rate
}

// Throttled returns the total time callers have spent waiting.
func (b *Budget) Throttled() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.throttle
}

// refill adds tokens for the time elapsed and lets a reduced rate recover by
// 1 % of the ceiling per second (Gmail's limits are per minute, so recovery
// is deliberately slow). Callers hold mu.
func (b *Budget) refill() {
	now := b.now()
	dt := now.Sub(b.last).Seconds()
	if dt <= 0 {
		return
	}
	b.last = now
	// After a quiet stretch try a higher ceiling (+10 %, at most every five
	// minutes): the limit may have been a burst of somebody else's traffic.
	if b.ceiling < b.max && now.Sub(b.lastEvent) >= probeEvery {
		b.ceiling = minFloat(b.max, b.ceiling*1.1)
		b.lastEvent = now
	}
	if b.rate < b.ceiling {
		b.rate = minFloat(b.ceiling, b.rate+b.max*0.01*dt)
	}
	b.tokens = minFloat(b.burst, b.tokens+b.rate*dt)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
