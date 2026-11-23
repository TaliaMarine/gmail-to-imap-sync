package progress

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestPublishFiltersAndNeverBlocks(t *testing.T) {
	h := NewHub(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	all, stopAll := h.Subscribe("")
	one, stopOne := h.Subscribe("a")
	other, stopOther := h.Subscribe("b")
	defer stopAll()
	defer stopOne()
	defer stopOther()

	h.Publish(Event{Kind: KindProgress, AccountID: "a", SessionID: "s"})
	if e := <-all; e.SessionID != "s" {
		t.Errorf("all: %+v", e)
	}
	if e := <-one; e.SessionID != "s" {
		t.Errorf("one: %+v", e)
	}
	select {
	case e := <-other:
		t.Errorf("other got %+v", e)
	default:
	}

	// A subscriber that never reads must not stall the publisher.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			h.Publish(Event{Kind: KindProgress, AccountID: "b"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
}

func TestUnsubscribeClosesChannelOnce(t *testing.T) {
	h := NewHub(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ch, stop := h.Subscribe("")
	stop()
	stop() // idempotent
	if _, ok := <-ch; ok {
		t.Error("channel not closed")
	}
	h.Publish(Event{}) // no subscribers left, no panic
}
