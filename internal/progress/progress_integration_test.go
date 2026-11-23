//go:build integration

package progress_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/progress"
)

func TestNotifyReachesSubscribersAcrossHubs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pg, err := postgres.Run(ctx, "postgres:17-alpine", postgres.WithDatabase("p"), postgres.WithUsername("p"),
		postgres.WithPassword("p"), postgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer func() { _ = pg.Terminate(context.Background()) }()
	url, _ := pg.ConnectionString(ctx, "sslmode=disable")
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); pool.Close() }() // the listener holds a connection until ctx ends

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := progress.NewHub(pool, log) // "the web pod"
	go hub.Run(ctx)
	all, stopAll := hub.Subscribe("")
	defer stopAll()
	one, stopOne := hub.Subscribe("acc-1")
	defer stopOne()
	other, stopOther := hub.Subscribe("acc-2")
	defer stopOther()
	time.Sleep(500 * time.Millisecond) // let LISTEN take effect

	// "The worker pod" only has the pool.
	if err := progress.Notify(ctx, pool, progress.Event{Kind: progress.KindProgress, AccountID: "acc-1", SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	for name, ch := range map[string]<-chan progress.Event{"all": all, "acc-1": one} {
		select {
		case e := <-ch:
			if e.SessionID != "s1" || e.Kind != progress.KindProgress {
				t.Errorf("%s: event %+v", name, e)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s: no event", name)
		}
	}
	select {
	case e := <-other:
		t.Errorf("acc-2 got another account's event: %+v", e)
	case <-time.After(300 * time.Millisecond):
	}
}
