package gmail

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu    sync.Mutex
	t     time.Time
	slept []time.Duration
	advTo bool // advance the clock when sleeping, like real time would
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0), advTo: true} }

func (f *fakeClock) now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.t }
func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}
func (f *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	f.slept = append(f.slept, d)
	if f.advTo {
		f.t = f.t.Add(d)
	}
	f.mu.Unlock()
	return nil
}

func testBudget(fraction float64) (*Budget, *fakeClock) {
	fc := newFakeClock()
	b := NewBudget(fraction)
	b.now, b.sleep, b.last = fc.now, fc.sleep, fc.now()
	return b, fc
}

func TestBudgetBurstThenPaces(t *testing.T) {
	b, fc := testBudget(0.4) // 100 units/s
	ctx := context.Background()
	// The first second's worth (100 units = 20 gets) is free.
	for i := 0; i < 20; i++ {
		if err := b.Wait(ctx, 5); err != nil {
			t.Fatal(err)
		}
	}
	if len(fc.slept) != 0 {
		t.Fatalf("burst should not wait, slept %v", fc.slept)
	}
	// The next get has to wait for its 5 units: 50ms at 100 units/s.
	if err := b.Wait(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if len(fc.slept) != 1 || fc.slept[0] != 50*time.Millisecond {
		t.Fatalf("slept %v, want [50ms]", fc.slept)
	}
	if b.Throttled() != 50*time.Millisecond {
		t.Errorf("throttled = %v", b.Throttled())
	}
}

func TestBudgetSteadyStateRate(t *testing.T) {
	b, fc := testBudget(0.2) // 50 units/s = 10 gets/s
	ctx := context.Background()
	start := fc.now()
	for i := 0; i < 110; i++ { // 550 units, 50 of them in the free burst
		if err := b.Wait(ctx, 5); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := fc.now().Sub(start).Seconds()
	if elapsed < 9.5 || elapsed > 10.5 { // (550-50)/50 = 10s
		t.Errorf("110 gets took %.2fs of budget time, want about 10s", elapsed)
	}
}

func TestBudgetLearnsACeilingBelowTheRateThatFailed(t *testing.T) {
	b, fc := testBudget(0.4) // max 100
	b.Slow()
	_, _, rate, ceiling := b.Stats()
	if ceiling != 80 || rate != 60 {
		t.Fatalf("after a hit at 100: ceiling %v rate %v, want 80 and 60", ceiling, rate)
	}
	// It recovers, but only up to the learned ceiling, not back to the configured maximum.
	fc.advance(2 * time.Minute)
	_, _, rate, ceiling = b.Stats()
	if rate != 80 || ceiling != 80 {
		t.Errorf("after two quiet minutes: rate %v ceiling %v, want 80/80", rate, ceiling)
	}
	// A hit while running at 80 lowers the ceiling again.
	b.Slow()
	if _, _, _, c := b.Stats(); c != 64 {
		t.Errorf("ceiling = %v, want 64", c)
	}
}

func TestBudgetProbesUpwardsDuringALongQuietRun(t *testing.T) {
	b, fc := testBudget(0.4)
	b.Slow() // ceiling 80
	b.Slow() // ceiling 48 (hit at rate 60)
	var prev float64
	for i := 0; i < 12; i++ { // an hour of quiet
		fc.advance(5 * time.Minute)
		_, _, _, c := b.Stats()
		if c < prev {
			t.Fatalf("ceiling fell without a hit: %v -> %v", prev, c)
		}
		prev = c
	}
	if _, _, rate, c := b.Stats(); c != 100 || rate != 100 {
		t.Errorf("after an hour of quiet: ceiling %v rate %v, want back at the maximum", c, rate)
	}
}

func TestBudgetNeverGoesBelowTheFloor(t *testing.T) {
	b, _ := testBudget(0.4)
	for i := 0; i < 30; i++ {
		b.Slow()
	}
	if r := b.Rate(); r < minRate {
		t.Errorf("rate %v below the floor", r)
	}
	hits, _, _, _ := b.Stats()
	if hits != 30 {
		t.Errorf("hits = %d", hits)
	}
}

func TestBudgetSlowDrainsBucket(t *testing.T) {
	b, fc := testBudget(0.4)
	b.Slow()
	if err := b.Wait(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if len(fc.slept) != 1 {
		t.Errorf("a request right after a rate-limit response must wait, slept %v", fc.slept)
	}
}

func TestBudgetCancelledWaitRefundsUnits(t *testing.T) {
	b, fc := testBudget(0.04)            // 10 units/s
	_ = b.Wait(context.Background(), 10) // empties the bucket
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Wait(ctx, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	// A wait that is cancelled while sleeping gives the units back.
	ctx2, cancel2 := context.WithCancel(context.Background())
	b.sleep = func(ctx context.Context, d time.Duration) error { cancel2(); return ctx.Err() }
	if err := b.Wait(ctx2, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	b.sleep = fc.sleep
	fc.slept = nil
	if err := b.Wait(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if len(fc.slept) != 1 || fc.slept[0] != 500*time.Millisecond {
		t.Errorf("slept %v, want exactly one 500ms wait (the cancelled 5 units were refunded)", fc.slept)
	}
}

func TestBudgetConcurrentCallersDoNotOvershoot(t *testing.T) {
	b, fc := testBudget(0.2) // 50 units/s
	fc.advTo = false         // time only moves when we say so
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ { // 200 units wanted, 50 available now
		wg.Add(1)
		go func() { defer wg.Done(); _ = b.Wait(context.Background(), 5) }()
	}
	wg.Wait()
	var total time.Duration
	var maxWait time.Duration
	for _, d := range fc.slept {
		total += d
		if d > maxWait {
			maxWait = d
		}
	}
	// The last caller owes 150 units at 50/s = 3s; none may owe more.
	if maxWait < 2900*time.Millisecond || maxWait > 3100*time.Millisecond {
		t.Errorf("longest wait %v, want about 3s", maxWait)
	}
}

func TestNewBudgetFractionDefaults(t *testing.T) {
	for _, f := range []float64{0, -1, 2} {
		if r := NewBudget(f).Rate(); r != 150 {
			t.Errorf("fraction %v: rate %v, want the 60%% default (150)", f, r)
		}
	}
}

func TestPauseStopsEveryoneUntilItEnds(t *testing.T) {
	b, fc := testBudget(0.4)
	b.Pause(20 * time.Second)
	b.Pause(5 * time.Second) // a shorter pause never shortens an existing one
	if err := b.Wait(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if len(fc.slept) != 1 || fc.slept[0] < 19*time.Second || fc.slept[0] > 21*time.Second {
		t.Errorf("slept %v, want one wait of about 20s", fc.slept)
	}
	// After the pause the bucket works as before.
	fc.slept = nil
	if err := b.Wait(context.Background(), 5); err != nil || len(fc.slept) != 0 {
		t.Errorf("after the pause: %v %v", err, fc.slept)
	}
}

func TestLearnedBudgetStartsNearWhatTheLastRunTolerated(t *testing.T) {
	b := NewLearnedBudget(0.6, 0.2) // configured 150 units/s; the last run settled at 0.2 (50 units/s)
	if _, _, rate, ceiling := b.Stats(); math.Abs(rate-55) > 1e-9 || math.Abs(ceiling-55) > 1e-9 {
		t.Errorf("rate %v ceiling %v, want both 55 (110%% of 50)", rate, ceiling)
	}
	// The configured value caps it; a learned value above it changes nothing.
	if _, _, rate, _ := NewLearnedBudget(0.3, 0.5).Stats(); rate != 75 {
		t.Errorf("rate = %v, want the configured 75", rate)
	}
	if _, _, rate, _ := NewLearnedBudget(0.3, 0).Stats(); rate != 75 {
		t.Errorf("rate = %v", rate)
	}
}
