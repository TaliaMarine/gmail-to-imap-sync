//go:build integration

package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
)

func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	ctx := context.Background()
	pg, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("gas"), postgres.WithUsername("gas"), postgres.WithPassword("gas"),
		postgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("postgres container unavailable: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })
	url, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

func TestMigrateIsIdempotentAndSeedsSettings(t *testing.T) {
	d := newTestDB(t)
	for i := 0; i < 2; i++ { // second run is a no-op
		if err := d.Migrate(); err != nil {
			t.Fatalf("migrate run %d: %v", i, err)
		}
	}
	s, err := d.GetAppSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.SessionRetentionDays != 365 || s.EventRetentionDays != 90 {
		t.Errorf("unexpected defaults: %+v", s)
	}
}

func TestOneActiveSessionPerAccount(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	if err := d.Migrate(); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) error { _, err := d.Pool.Exec(ctx, sql, args...); return err }
	if err := exec(`INSERT INTO accounts (id, name) VALUES ('00000000-0000-0000-0000-000000000001', 'a')`); err != nil {
		t.Fatal(err)
	}
	ins := `INSERT INTO sync_sessions (id, account_id, status, trigger_type, sync_type)
	        VALUES ($1, '00000000-0000-0000-0000-000000000001', $2, 'manual', 'incremental')`
	if err := exec(ins, "00000000-0000-0000-0000-0000000000a1", "queued"); err != nil {
		t.Fatal(err)
	}
	if err := exec(ins, "00000000-0000-0000-0000-0000000000a2", "running"); err == nil {
		t.Error("second active session accepted")
	}
	if err := exec(ins, "00000000-0000-0000-0000-0000000000a3", "completed"); err != nil {
		t.Errorf("finished sessions must not conflict: %v", err)
	}
}

func TestFailedMessageUnique(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	if err := d.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO accounts (id, name) VALUES ('00000000-0000-0000-0000-000000000001', 'a')`); err != nil {
		t.Fatal(err)
	}
	ins := `INSERT INTO failed_messages (id, account_id, gmail_message_id, error_category)
	        VALUES ($1, '00000000-0000-0000-0000-000000000001', 'm1', 'imap_append')`
	if _, err := d.Pool.Exec(ctx, ins, "00000000-0000-0000-0000-0000000000f1"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, ins, "00000000-0000-0000-0000-0000000000f2"); err == nil {
		t.Error("duplicate failure row for one message accepted")
	}
}

func TestCheckpointQueriesKeepTheTimeOfTheHistoryRead(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	if err := d.Migrate(); err != nil {
		t.Fatal(err)
	}
	acct := "00000000-0000-0000-0000-000000000001"
	if _, err := d.Pool.Exec(ctx, `INSERT INTO accounts (id, name) VALUES ($1, 'a')`, acct); err != nil {
		t.Fatal(err)
	}
	var id = uuid.MustParse(acct)
	if err := d.EnsureSyncState(ctx, id); err != nil {
		t.Fatal(err)
	}
	// The start history ID and when it was read are kept by a resumed import.
	if err := d.BeginInitialSync(ctx, store.BeginInitialSyncParams{AccountID: id, StartHistoryID: pgtype.Int8{Int64: 10, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	st, _ := d.GetSyncState(ctx, id)
	first := st.StartHistoryAt
	if !first.Valid || st.StartHistoryID.Int64 != 10 {
		t.Fatalf("state = %+v", st)
	}
	time.Sleep(20 * time.Millisecond)
	if err := d.BeginInitialSync(ctx, store.BeginInitialSyncParams{AccountID: id, StartHistoryID: pgtype.Int8{Int64: 99, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	st, _ = d.GetSyncState(ctx, id)
	if st.StartHistoryID.Int64 != 10 || !st.StartHistoryAt.Time.Equal(first.Time) {
		t.Errorf("a resumed import must keep its first start: %+v", st)
	}
	if err := d.CompleteInitialSync(ctx, id); err != nil {
		t.Fatal(err)
	}
	st, _ = d.GetSyncState(ctx, id)
	if st.LastSuccessfulHistoryID.Int64 != 10 || !st.HistoryCheckpointAt.Time.Equal(first.Time) {
		t.Errorf("checkpoint time must be when the history ID was read: %+v", st)
	}
	// A later final checkpoint moves both; a lower ID changes nothing; a plain rescan touch changes only the display time.
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	if err := d.SaveFinalCheckpoint(ctx, store.SaveFinalCheckpointParams{AccountID: id, HistoryID: pgtype.Int8{Int64: 20, Valid: true}, At: pgtype.Timestamptz{Time: at, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveFinalCheckpoint(ctx, store.SaveFinalCheckpointParams{AccountID: id, HistoryID: pgtype.Int8{Int64: 5, Valid: true}, At: pgtype.Timestamptz{Time: time.Now(), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := d.TouchLastSuccess(ctx, id); err != nil {
		t.Fatal(err)
	}
	st, _ = d.GetSyncState(ctx, id)
	if st.LastSuccessfulHistoryID.Int64 != 20 || !st.HistoryCheckpointAt.Time.Equal(at) {
		t.Errorf("state = %+v", st)
	}
	// Reauthorising another mailbox starts from scratch.
	if err := d.ResetInitialSync(ctx, id); err != nil {
		t.Fatal(err)
	}
	st, _ = d.GetSyncState(ctx, id)
	if st.InitialSyncCompleted || st.LastSuccessfulHistoryID.Valid || st.HistoryCheckpointAt.Valid || st.StartHistoryID.Valid {
		t.Errorf("state after reset = %+v", st)
	}
}

func TestDueFailuresHonoursRequestedRetriesOfPermanentCategories(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	if err := d.Migrate(); err != nil {
		t.Fatal(err)
	}
	acct := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	if _, err := d.Pool.Exec(ctx, `INSERT INTO accounts (id, name) VALUES ($1, 'a')`, acct); err != nil {
		t.Fatal(err)
	}
	add := func(msgID, category string) uuid.UUID {
		row, err := d.UpsertFailure(ctx, store.UpsertFailureParams{ID: uuid.New(), AccountID: acct, GmailMessageID: msgID,
			ErrorCategory: category, Recipients: []byte("[]"), IntendedFolders: []byte("[]"), CompletedFolders: []byte("[]"),
			FailedFolders: []byte("[]"), ErrorHistory: []byte("[]")})
		if err != nil {
			t.Fatal(err)
		}
		return row.ID
	}
	temp, perm := add("m1", "imap_append"), add("m2", "imap_auth")
	due := func() map[uuid.UUID]bool {
		rows, err := d.DueFailures(ctx, store.DueFailuresParams{AccountID: acct, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		out := map[uuid.UUID]bool{}
		for _, r := range rows {
			out[r.ID] = true
		}
		return out
	}
	if got := due(); !got[temp] || got[perm] {
		t.Fatalf("due = %v", got)
	}
	if err := d.ResetFailureForRetry(ctx, perm); err != nil {
		t.Fatal(err)
	}
	if got := due(); !got[perm] {
		t.Error("a retry asked for by a person must be attempted even for a permanent category")
	}
	// Failing again clears the request: it does not loop.
	add("m2", "imap_auth")
	if got := due(); got[perm] {
		t.Error("the request is spent after one attempt")
	}
	keys, err := d.PendingFailureKeys(ctx, acct)
	if err != nil || len(keys) != 2 {
		t.Errorf("keys = %v, %v", keys, err)
	}
}
