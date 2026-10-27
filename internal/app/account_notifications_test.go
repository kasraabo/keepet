package app

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccountNotifications(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	u, e := url.Parse(dsn)
	if e != nil || !strings.HasSuffix(u.Path, "_test") {
		t.Fatal("isolated test database required")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("APP_MODE", "development")
	t.Setenv("ADMIN_SETTINGS_KEY", "")
	t.Setenv("SETTINGS_KEY_FILE", filepath.Join(t.TempDir(), "settings.key"))
	ctx := context.Background()
	a, e := New(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := a.DB.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	scalar := func(q string, args ...any) int64 {
		t.Helper()
		var n int64
		if e := a.DB.QueryRow(ctx, q, args...).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	exec("TRUNCATE users CASCADE")
	if e = a.seed(ctx); e != nil {
		t.Fatal(e)
	}
	exec("INSERT INTO users(id,phone,name,role) VALUES('alert-owner','09123334440','آزمایش اعلان','owner'),('alert-other','09123334441','دیگر','owner')")
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	actor := func(phone string) *browser { b := newBrowser(t, srv); b.login(phone); return b }
	owner, other, ops, admin, editor := actor("09123334440"), actor("09123334441"), actor("09120000001"), actor("09120000005"), actor("09120000007")
	post := func(b *browser, path string, v url.Values, want int) string {
		t.Helper()
		h, c := b.post(path, v)
		if c != want {
			t.Fatalf("%s %d want %d %.1000s", path, c, want, h)
		}
		return h
	}
	count := func(kind string) int64 { return scalar("SELECT count(*) FROM notifications WHERE kind=$1", kind) }
	ledger := `SELECT wallet_post('alert-owner','admin_cash',1000000,0,0,'notification-funds',NULL,NULL,'PRIVATE-FINANCIAL-REASON')`
	exec(ledger)
	exec(ledger)
	if count("wallet_entry") != 1 {
		t.Fatal("ledger notification duplicated")
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `SELECT wallet_post('alert-owner','admin_bonus',0,50,0,'rollback-alert',NULL,NULL,'private')`); e != nil {
		t.Fatal(e)
	}
	tx.Rollback(ctx)
	if count("wallet_entry") != 1 || scalar("SELECT cash FROM wallets WHERE user_id='alert-owner'") != 1000000 {
		t.Fatal("rollback or balance changed")
	}
	exec("UPDATE wallets SET frozen=true WHERE user_id='alert-owner'")
	exec("UPDATE wallets SET frozen=true WHERE user_id='alert-owner'")
	exec("UPDATE wallets SET frozen=false WHERE user_id='alert-owner'")
	if count("wallet_account") != 2 {
		t.Fatal("freeze no-op duplicated")
	}
	post(owner, "/wallet/bank", url.Values{"holder": {"آزمایش اعلان"}, "iban": {"IR062960000000100324200001"}}, 200)
	var bank string
	if e = a.DB.QueryRow(ctx, "SELECT id FROM wallet_bank_accounts WHERE user_id='alert-owner'").Scan(&bank); e != nil {
		t.Fatal(e)
	}
	if count("bank_account") != 1 || count("wallet_operations") != 2 {
		t.Fatal("bank recipients")
	}
	post(admin, "/management/wallet/banks/"+bank, url.Values{"status": {"verified"}, "revision": {"1"}, "note": {"PRIVATE-BANK-REVIEW"}, "attest": {"on"}}, 200)
	post(owner, "/wallet/withdrawals", url.Values{"amount": {"200000"}, "request_key": {token()}}, 200)
	var withdrawal string
	a.DB.QueryRow(ctx, "SELECT id FROM wallet_withdrawals WHERE user_id='alert-owner'").Scan(&withdrawal)
	if count("withdrawal") != 1 || count("wallet_operations") != 4 {
		t.Fatal("withdrawal recipients")
	}
	if scalar("SELECT cash+reserved FROM wallets WHERE user_id='alert-owner'") != 1000000 {
		t.Fatal("notification changed financial total")
	}
	post(ops, "/management/wallet/withdrawals/"+withdrawal, url.Values{"action": {"claim"}}, 200)
	post(admin, "/management/wallet/withdrawals/"+withdrawal, url.Values{"action": {"reset"}, "reason": {"PRIVATE-RESET"}, "no_transfer": {"on"}}, 200)
	if count("withdrawal") != 3 || count("wallet_operations") != 6 {
		t.Fatal("reset notification must be a distinct event")
	}
	post(owner, "/wallet/withdrawals/"+withdrawal+"/cancel", nil, 200)
	if count("withdrawal") != 4 || scalar("SELECT cash FROM wallets WHERE user_id='alert-owner'") != 1000000 {
		t.Fatal("cancel notification or released funds")
	}
	if count("wallet_entry") != 1 {
		t.Fatal("withdrawal reserve/release duplicate ledger alerts")
	}
	// Direct source filters do not depend on the current page and preserve ownership.
	entryID := scalar("SELECT id FROM wallet_entries WHERE user_id='alert-owner' LIMIT 1")
	entries, err := a.walletEntries(ctx, "alert-owner", "", 999, 100, fmt.Sprint(entryID))
	if err != nil || len(entries) != 1 {
		t.Fatal("old ledger target", err)
	}
	requests, err := a.walletWithdrawals(ctx, "alert-owner", "", 999, withdrawal)
	if err != nil || len(requests) != 1 {
		t.Fatal("old withdrawal target", err)
	}
	requests, err = a.walletWithdrawals(ctx, "alert-other", "", 0, withdrawal)
	if err != nil || len(requests) != 0 {
		t.Fatal("cross-user withdrawal target", err)
	}
	// An old ticket outside the first page must be reachable directly.
	exec("INSERT INTO tickets(id,user_id,subject,body,email,created_at) VALUES('old-alert-ticket','alert-owner','تیکت قدیمی','PRIVATE-TICKET-BODY','private@example.test',now()-interval '5 days')")
	exec("INSERT INTO tickets(id,user_id,subject,body,email) SELECT 'noise-'||n,'alert-other','جدید','body','other@example.test' FROM generate_series(1,30)n")
	if h, c := ops.get("/ops?ticket=old-alert-ticket#tickets"); c != 200 || !strings.Contains(h, `id="ticket-old-alert-ticket"`) || strings.Contains(h, `id="ticket-noise-`) {
		t.Fatal("ticket deep link", c)
	}
	post(ops, "/ops/tickets/old-alert-ticket", url.Values{"reply": {"پاسخ خصوصی پشتیبانی"}}, 200)
	post(ops, "/ops/tickets/old-alert-ticket", url.Values{"reply": {"پاسخ خصوصی پشتیبانی"}}, 200)
	if count("ticket") != 1 {
		t.Fatal("unchanged ticket reply duplicated")
	}
	post(ops, "/ops/tickets/missing-ticket", url.Values{"reply": {"پاسخ"}}, 404)
	// No-op audits and unrelated administrative audits must not notify editors.
	audit := `INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES('demo-admin',$1,'alert-article',$2::jsonb,$3::jsonb)`
	exec(audit, "article", `{"enabled":false}`, `{"enabled":false}`)
	exec(audit, "wallet_adjust", `{}`, `{"cash":5}`)
	if count("editorial") != 0 {
		t.Fatal("irrelevant/no-op audit")
	}
	exec(audit, "article", `{"enabled":false}`, `{"enabled":true,"body":"PRIVATE-DRAFT"}`)
	if count("editorial") != 1 {
		t.Fatal("editor recipient or self notification")
	}
	exec("INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES('demo-content','magazine_layout','home','{\"Revision\":1,\"First\":[]}','{\"Revision\":2,\"First\":[]}')")
	if count("editorial") != 1 {
		t.Fatal("revision-only audit notified")
	}
	for _, b := range []*browser{owner, ops, admin, editor} {
		h, c := b.get("/notifications")
		if c != 200 {
			t.Fatal(c)
		}
		for _, secret := range []string{"PRIVATE-", "private@example.test", "IR062960"} {
			if strings.Contains(h, secret) {
				t.Fatal("notification leaked source payload", secret)
			}
		}
	}
	if h, c := other.get("/notifications"); c != 200 || strings.Contains(h, "رسید پرداخت") || strings.Contains(h, "کیف پول محدود") {
		t.Fatal("other user's wallet alerts")
	}
	// Read-one is owner-scoped, and role revocation affects list/count/read/read-all immediately.
	id := scalar("SELECT id FROM notifications WHERE kind='wallet_entry' LIMIT 1")
	post(other, fmt.Sprintf("/notifications/%d/read", id), nil, 404)
	post(owner, fmt.Sprintf("/notifications/%d/read", id), nil, 200)
	staffID := scalar("SELECT min(id) FROM notifications WHERE user_id='demo-ops' AND kind='wallet_operations'")
	exec("UPDATE users SET role='owner' WHERE id='demo-ops'")
	post(ops, fmt.Sprintf("/notifications/%d/read", staffID), nil, 404)
	post(ops, "/notifications/all/read", url.Values{"before_id": {"999999999"}}, 200)
	if scalar("SELECT count(*) FROM notifications WHERE user_id='demo-ops' AND read_at IS NOT NULL") != 0 {
		t.Fatal("revoked role read-all changed hidden alerts")
	}
	if h, c := ops.get("/notifications/count"); c != 200 || !strings.Contains(h, `"unread":0`) {
		t.Fatal("revoked role unread", h)
	}
	exec("UPDATE users SET role='content' WHERE id='demo-ops'")
	exec("UPDATE users SET role='owner' WHERE id='demo-content'")
	if h, c := editor.get("/notifications"); c != 200 || strings.Contains(h, "محتوا منتشر شد") {
		t.Fatal("editor access after role revocation")
	}
	// Restoring staff role keeps the unmodified pending notices available.
	exec("UPDATE users SET role='ops' WHERE id='demo-ops'")
	post(ops, fmt.Sprintf("/notifications/%d/read", staffID), nil, 200)
}
