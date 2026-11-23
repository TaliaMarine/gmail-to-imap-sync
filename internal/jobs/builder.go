package jobs

import (
	"context"
	"errors"
	"fmt"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/clients"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/gmail"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/imapdest"
)

// DBBuilder builds a Gmail client and an open destination session from the
// stored account, settings and destination.
type DBBuilder struct {
	DB      *db.DB
	Clients *clients.Factory
}

// Build implements Builder.
func (b *DBBuilder) Build(ctx context.Context, sess store.SyncSession) (Resources, error) {
	acct, err := b.DB.GetAccount(ctx, sess.AccountID)
	if err != nil {
		return Resources{}, fmt.Errorf("load account: %w", err)
	}
	cfg, err := b.DB.GetSyncSettings(ctx, sess.AccountID)
	if err != nil {
		return Resources{}, fmt.Errorf("load sync settings: %w", err)
	}
	if !acct.DestinationID.Valid {
		return Resources{}, errors.New("the account has no destination")
	}
	dest, err := b.DB.GetDestination(ctx, acct.DestinationID.UUID)
	if err != nil {
		return Resources{}, fmt.Errorf("load destination: %w", err)
	}
	if !dest.Enabled {
		return Resources{}, errors.New("the destination is disabled")
	}

	// Start where an earlier session learned the account tolerates, if lower.
	learned := 0.0
	if st, err := b.DB.GetSyncState(ctx, sess.AccountID); err == nil && st.LearnedRateFraction.Valid {
		learned = float64(st.LearnedRateFraction.Float32)
	}
	gm, err := b.Clients.Gmail(ctx, acct, gmail.NewLearnedBudget(float64(cfg.RateFraction), learned))
	if err != nil {
		return Resources{}, err
	}
	imapCfg, err := b.Clients.IMAPConfig(dest)
	if err != nil {
		return Resources{}, err
	}
	conns := int(cfg.WorkerCount)
	if conns > 8 {
		conns = 8
	}
	sessn, err := imapdest.Open(ctx, imapdest.Options{Config: imapCfg, Conns: conns, Subscribe: cfg.SubscribeFolders})
	if err != nil {
		return Resources{}, err
	}
	if sessn.Delimiter() != dest.HierarchyDelimiter {
		_ = b.DB.SetDestinationDelimiter(ctx, store.SetDestinationDelimiterParams{ID: dest.ID, HierarchyDelimiter: sessn.Delimiter()})
	}
	return Resources{Gmail: gm, Dest: sessn, Close: func() { _ = sessn.Close() }}, nil
}
