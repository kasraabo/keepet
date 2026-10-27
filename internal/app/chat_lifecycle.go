package app

import (
	"context"
	"log"
	"time"
)

// Expiry is enforced on every write, even between maintenance ticks. Maintenance
// persists the archive and completes visits with a submitted clinical report.
func (a *App) startChatMaintenance() {
	ctx, cancel := context.WithCancel(context.Background())
	a.chatCancel = cancel
	a.chatDone = make(chan struct{})
	go func() {
		defer close(a.chatDone)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				work, stop := context.WithTimeout(ctx, 10*time.Second)
				if err := a.reconcileRequests(work); err != nil && ctx.Err() == nil {
					log.Printf("request maintenance: %v", err)
				}
				if err := a.reconcileFulfillments(work); err != nil && ctx.Err() == nil {
					log.Printf("service completion: %v", err)
				}
				if err := a.reconcileChats(work); err != nil && ctx.Err() == nil {
					log.Printf("consultation expiry: %v", err)
				}
				stop()
			}
		}
	}()
}

func (a *App) reconcileChats(ctx context.Context) error {
	rows, err := a.DB.Query(ctx, `SELECT c.id FROM consultation_chats c JOIN bookings b ON b.id=c.booking_id WHERE (c.closed_at IS NULL AND NOT chat_is_open(c,b)) OR (c.kind='vet' AND c.closed_at IS NOT NULL AND b.status IN ('in_progress','awaiting_report') AND EXISTS(SELECT 1 FROM consultation_vet_forms WHERE chat_id=c.id)) ORDER BY c.booking_id LIMIT 100`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		tx, e := a.DB.Begin(ctx)
		if e != nil {
			return e
		}
		_, _, e = a.lockedChat(ctx, tx, id, &User{Role: "admin"})
		if e == nil {
			e = tx.Commit(ctx)
		}
		tx.Rollback(ctx)
		if e != nil && e != errChatNotFound {
			return e
		}
	}
	return nil
}
