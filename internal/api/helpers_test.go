package api

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/mapping"
)

func TestCursorRoundTrip(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	at := time.Date(2024, 5, 6, 7, 8, 9, 123456000, time.UTC)
	c, err := decodeCursor(encodeCursor(at, id))
	if err != nil || !c.T.Equal(at) || c.ID != id {
		t.Fatalf("cursor = %+v, %v", c, err)
	}
	for _, bad := range []string{"", "!!!", "e30", encodeCursor(time.Time{}, id), encodeCursor(at, uuid.Nil)} {
		if _, err := decodeCursor(bad); err == nil {
			t.Errorf("decodeCursor(%q) accepted", bad)
		}
	}
}

func TestPageSize(t *testing.T) {
	for in, want := range map[int]int32{-1: defaultPageSize, 0: defaultPageSize, 10: 10, 200: 200, 5000: maxPageSize} {
		if got := pageSize(in); got != want {
			t.Errorf("pageSize(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestFailureCategoriesAreDescribed(t *testing.T) {
	// Every category the engine can produce has a human description.
	for _, c := range []string{"gmail_rate_limit", "gmail_auth", "gmail_fetch", "gmail_message_unavailable",
		"imap_auth", "imap_connection", "imap_folder", "imap_append", "parse", "config", "unknown"} {
		if categoryInfo(c).Description == "" {
			t.Errorf("category %s has no description", c)
		}
	}
	if categoryInfo("nonsense").Description != failureCategories["unknown"].Description {
		t.Error("unknown categories must fall back to the generic description")
	}
	// Categories that need a human must not claim automatic retry.
	for _, c := range []string{"gmail_auth", "imap_auth", "config", "parse", "gmail_message_unavailable"} {
		if categoryInfo(c).AutoRetry {
			t.Errorf("%s must not auto-retry", c)
		}
	}
}

func TestFailureBodyNeverCarriesMessageContent(t *testing.T) {
	f := store.FailedMessage{
		ID: uuid.New(), AccountID: uuid.New(), GmailMessageID: "m1", Subject: "Hello", Sender: "a@x",
		IntendedFolders: []byte(`["INBOX","Work"]`), CompletedFolders: []byte(`["INBOX"]`), FailedFolders: []byte(`["Work"]`),
		Recipients: []byte(`["b@x"]`), ErrorHistory: []byte(`[{"category":"imap_append"}]`),
		ErrorCategory: "imap_append", Status: "pending", AttemptCount: 2,
	}
	d := failureDetail(f)
	if len(d.IntendedFolders) != 2 || d.FailedFolders[0] != "Work" || d.Recipients[0] != "b@x" || !d.AutoRetry {
		t.Errorf("detail = %+v", d)
	}
	if string(d.ErrorHistory) != `[{"category":"imap_append"}]` {
		t.Errorf("history = %s", d.ErrorHistory)
	}
	empty := failureDetail(store.FailedMessage{})
	if string(empty.ErrorHistory) != "[]" || empty.IntendedFolders == nil {
		t.Error("empty collections must be [] not null")
	}
}

func TestSyncSettingsRoundTripAndValidation(t *testing.T) {
	row := store.AccountSyncSetting{
		Enabled: true, ScheduleIntervalSeconds: 7200, ArchiveFolder: "Archive", SentFolder: "Sent", InboxFolder: "INBOX",
		ExcludedLabels: []byte(`[{"kind":"id","value":"SPAM"}]`), LabelMapping: []byte(`{"L1":"Clients"}`),
		FlagMapping:      []byte(`{"unread_as_seen":true,"starred_as_flagged":false,"folder_labels":null}`),
		InitialSyncAfter: pgtype.Date{Time: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), Valid: true},
	}
	s, err := syncSettingsBody(row)
	if err != nil {
		t.Fatal(err)
	}
	if *s.InitialSyncAfter != "2024-01-02" || s.LabelMapping["L1"] != "Clients" || s.FlagMapping.FolderLabels == nil || len(s.ExcludedLabels) != 1 {
		t.Errorf("settings = %+v", s)
	}
	if err := s.validate("/"); err != nil {
		t.Errorf("valid settings rejected: %v", err)
	}

	bad := s
	bad.InitialSyncAfter = ptr("yesterday")
	if bad.validate("/") == nil {
		t.Error("bad date accepted")
	}
	bad = s
	bad.ExcludedLabels = []mapping.Matcher{{Kind: "regex", Value: "x"}}
	if bad.validate("/") == nil {
		t.Error("bad matcher kind accepted")
	}
	bad = s
	bad.ArchiveFolder = " / "
	if bad.validate("/") == nil {
		t.Error("unusable folder accepted")
	}
	if _, err := syncSettingsBody(store.AccountSyncSetting{ExcludedLabels: []byte("{"), LabelMapping: []byte("{}"), FlagMapping: []byte("{}")}); err == nil {
		t.Error("corrupt column accepted")
	}

	ms := s.mappingSettings(".")
	if ms.Delimiter != "." || ms.InboxFolder != "INBOX" || !ms.UnreadAsSeen || ms.StarredAsFlagged {
		t.Errorf("mapping settings = %+v", ms)
	}
}

func ptr[T any](v T) *T { return &v }

func TestFillActivity(t *testing.T) {
	since := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	rows := []store.SessionActivityByDayRow{
		{Day: pgtype.Timestamptz{Time: since.AddDate(0, 0, 1), Valid: true}, Copied: 5, Sessions: 2},
		{Day: pgtype.Timestamptz{Time: since.AddDate(0, 0, 3), Valid: true}, Failed: 1, Sessions: 1},
	}
	got := fillActivity(rows, since, 4)
	if len(got) != 4 || got[0].Sessions != 0 || got[1].Copied != 5 || got[2].Sessions != 0 || got[3].Failed != 1 {
		t.Errorf("activity = %+v", got)
	}
	if !got[0].Day.Equal(since) {
		t.Errorf("first day = %v", got[0].Day)
	}
}

func TestPgErrorCodes(t *testing.T) {
	if !isUniqueViolation(&pgconn.PgError{Code: "23505"}) || !isFKViolation(&pgconn.PgError{Code: "23503"}) {
		t.Error("codes not recognised")
	}
	if isUniqueViolation(errors.New("x")) || isUniqueViolation(nil) {
		t.Error("false positive")
	}
}

func TestSessionSummaryHidesNothingSensitiveAndMapsNulls(t *testing.T) {
	s := sessionSummary(store.SyncSession{ID: uuid.New(), AccountID: uuid.New(), Status: "running", CopiedCount: 3,
		CancelRequestedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}})
	if s.Copied != 3 || !s.CancelRequested || s.StartedAt != nil || s.StartingHistoryID != nil {
		t.Errorf("summary = %+v", s)
	}
}

func TestShortError(t *testing.T) {
	long := errors.New(string(make([]byte, 1000)))
	if got := shortError(long); len(got) > 310 {
		t.Errorf("length = %d", len(got))
	}
}
