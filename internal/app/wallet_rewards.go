package app

import (
	"context"
	"errors"
	"strings"
)

// GrantReferralReward is a server-only integration point for a future verified
// referral event. Never call this from a user-supplied referral code alone.
// The event ID must identify one reward recipient and remain stable on retries.
// Rewards are promotional credit, so a refund cannot turn them into cash.
func (a *App) GrantReferralReward(ctx context.Context, userID, eventID string, amount int64) error {
	if len(eventID) < 3 || len(eventID) > 160 || strings.TrimSpace(userID) == "" || amount < 1 || amount > 1000000000 {
		return errors.New("invalid verified reward event")
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = walletPost(ctx, tx, userID, "referral_reward", 0, amount, 0, "referral:"+eventID, "", "", "پاداش رویداد معرفی تأییدشده"); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
