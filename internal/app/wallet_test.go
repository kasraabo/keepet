package app

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWalletValidation(t *testing.T) {
	if !validIBAN("IR062960000000100324200001") || validIBAN("IR072960000000100324200001") || validIBAN("IR00123") {
		t.Fatal("Iran IBAN checksum")
	}
	for _, s := range []string{"-1", "1.5", "1000000001", "NaN", "1,5"} {
		if _, e := walletAmount(s); e == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	if n, e := walletAmount("۱۲۳۴"); e != nil || n != 1234 {
		t.Fatal("Persian amount")
	}
	if n, e := walletAmount("۱٬۲۳۴٬۵۶۷"); e != nil || n != 1234567 {
		t.Fatal("grouped Persian amount")
	}
	if bankName("دکتر نازنین مهر") != bankName("نازنین مهر") {
		t.Fatal("honorific mismatch")
	}
	if !validIBAN(normalizeIBAN("‎۰۶۲۹۶۰۰۰۰۰۰۰۱۰۰۳۲۴۲۰۰۰۰۱")) {
		t.Fatal("24-digit Persian IBAN")
	}
	if bankName("علي  كاظمی") != bankName("علی کاظمی") {
		t.Fatal("bank name normalization")
	}
	if money(-100) != "-۱۰۰" {
		t.Fatal("negative money formatting")
	}
}
func TestWallet(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated test database")
	}
	parsed, e := url.Parse(dsn)
	if e != nil || !strings.HasSuffix(parsed.Path, "_test") {
		t.Fatal("refusing non-test database")
	}
	t.Setenv("APP_MODE", "development")
	t.Setenv("DATABASE_URL", dsn)
	ctx := context.Background()
	a, e := New(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	if _, e = a.DB.Exec(ctx, "TRUNCATE users CASCADE; UPDATE wallet_settings SET min_withdrawal=100000,max_withdrawal=50000000,daily_limit=100000000,withdrawals_enabled=true"); e != nil {
		t.Fatal(e)
	}
	if e = a.seed(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = a.DB.Exec(ctx, "INSERT INTO users(id,phone,name,role) VALUES('wallet-owner','09121111000','آزمایش مشتری','owner'),('wallet-other','09121111001','مشتری دیگر','owner')"); e != nil {
		t.Fatal(e)
	}
	s := httptest.NewServer(a.Handler())
	defer s.Close()
	owner := newBrowser(t, s)
	owner.login("09121111000")
	other := newBrowser(t, s)
	other.login("09121111001")
	admin := newBrowser(t, s)
	admin.login("09120000005")
	ops := newBrowser(t, s)
	ops.login("09120000001")
	vet := newBrowser(t, s)
	vet.login("09120000002")
	post := func(b *browser, path string, v url.Values, want int) {
		t.Helper()
		h, c := b.post(path, v)
		if c != want {
			t.Fatalf("%s got %d want %d: %.500s", path, c, want, h)
		}
	}
	balance := func(uid string, cash, bonus, res int64) {
		t.Helper()
		var c, b, r int64
		e := a.DB.QueryRow(ctx, "SELECT cash,bonus,reserved FROM wallets WHERE user_id=$1", uid).Scan(&c, &b, &r)
		if e != nil || c != cash || b != bonus || r != res {
			t.Fatalf("%s balance %d/%d/%d expected %d/%d/%d %v", uid, c, b, r, cash, bonus, res, e)
		}
	}
	t.Run("role boundaries and rendering", func(t *testing.T) {
		for _, b := range []*browser{owner, vet, ops, admin} {
			if h, c := b.get("/wallet"); c != 200 || !strings.Contains(h, "گردش کیف پول") {
				t.Fatalf("wallet render %d %.200s", c, h)
			}
		}
		if _, c := owner.get("/management/wallet"); c != 403 {
			t.Fatal("owner management access")
		}
		for _, b := range []*browser{admin, ops} {
			if h, c := b.get("/management/wallet"); c != 200 || !strings.Contains(h, "بررسی دستی مالکیت شبا") {
				t.Fatalf("admin render %d %.200s", c, h)
			}
		}
		post(ops, "/management/wallet/users/wallet-owner/adjust", url.Values{}, 403)
		post(owner, "/wallet/topup", url.Values{"amount": {"100000"}, "request_key": {token()}, "csrf": {"bad"}}, 403)
	})
	t.Run("credits idempotent and immutable", func(t *testing.T) {
		if e = a.GrantReferralReward(ctx, "wallet-owner", "confirmed-event-1", 50000); e != nil {
			t.Fatal(e)
		}
		if e = a.GrantReferralReward(ctx, "wallet-owner", "confirmed-event-1", 50000); e != nil {
			t.Fatal(e)
		}
		if e = a.GrantReferralReward(ctx, "wallet-owner", "confirmed-event-1", 60000); e == nil {
			t.Fatal("changed reward accepted")
		}
		v := url.Values{"amount": {"1000000"}, "request_key": {token()}, "pool": {"cash"}, "direction": {"credit"}, "reason": {"اصلاح تأییدشدهٔ تست"}}
		post(admin, "/management/wallet/users/wallet-owner/adjust", v, 200)
		post(admin, "/management/wallet/users/wallet-owner/adjust", v, 200)
		balance("wallet-owner", 1000000, 50000, 0)
		if _, e = a.DB.Exec(ctx, "UPDATE wallet_entries SET cash_delta=0 WHERE user_id='wallet-owner'"); e == nil {
			t.Fatal("ledger mutation allowed")
		}
		if _, e = a.DB.Exec(ctx, "DELETE FROM wallet_entries WHERE user_id='wallet-owner'"); e == nil {
			t.Fatal("ledger deletion allowed")
		}
	})
	const iban = "IR062960000000100324200001"
	var bankID string
	var rev int64
	t.Run("bank validation, manual verification and stale revisions", func(t *testing.T) {
		post(owner, "/wallet/bank", url.Values{"iban": {iban}, "holder": {"نام شخص دیگر"}}, 400)
		post(owner, "/wallet/bank", url.Values{"iban": {"IR072960000000100324200001"}, "holder": {"آزمایش مشتری"}}, 400)
		post(owner, "/wallet/bank", url.Values{"iban": {iban}, "holder": {"آزمایش مشتری"}}, 200)
		post(owner, "/wallet/withdrawals", url.Values{"amount": {"200000"}, "request_key": {token()}}, 409)
		a.DB.QueryRow(ctx, "SELECT id,revision FROM wallet_bank_accounts WHERE user_id='wallet-owner'").Scan(&bankID, &rev)
		post(admin, "/management/wallet/banks/"+bankID, url.Values{"status": {"verified"}, "revision": {fmt.Sprint(rev)}, "note": {"بررسی نام ذی‌نفع در بانک"}}, 400)
		post(admin, "/management/wallet/banks/"+bankID, url.Values{"status": {"verified"}, "revision": {"999"}, "note": {"بررسی نام ذی‌نفع در بانک"}, "attest": {"on"}}, 409)
		post(admin, "/management/wallet/banks/"+bankID, url.Values{"status": {"verified"}, "revision": {fmt.Sprint(rev)}, "note": {"بررسی نام ذی‌نفع در بانک"}, "attest": {"on"}}, 200)
	})
	var withdrawal string
	t.Run("concurrent withdrawal cannot overspend and release once", func(t *testing.T) {
		codes := make(chan int, 2)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, c := owner.post("/wallet/withdrawals", url.Values{"amount": {"700000"}, "request_key": {token()}})
				codes <- c
			}()
		}
		wg.Wait()
		close(codes)
		ok, conflict := 0, 0
		for c := range codes {
			if c == 200 {
				ok++
			} else if c == 409 {
				conflict++
			}
		}
		if ok != 1 || conflict != 1 {
			t.Fatalf("withdrawal race %d/%d", ok, conflict)
		}
		balance("wallet-owner", 300000, 50000, 700000)
		a.DB.QueryRow(ctx, "SELECT id FROM wallet_withdrawals WHERE user_id='wallet-owner' AND status='pending'").Scan(&withdrawal)
		post(other, "/wallet/withdrawals/"+withdrawal+"/cancel", nil, 404)
		post(owner, "/wallet/withdrawals/"+withdrawal+"/cancel", nil, 200)
		post(owner, "/wallet/withdrawals/"+withdrawal+"/cancel", nil, 409)
		balance("wallet-owner", 1000000, 50000, 0)
	})
	t.Run("unchanged verified IBAN stays verified", func(t *testing.T) {
		post(owner, "/wallet/bank", url.Values{"iban": {"۰۶۲۹۶۰۰۰۰۰۰۰۱۰۰۳۲۴۲۰۰۰۰۱"}, "holder": {"آزمایش مشتری"}}, 200)
		var st string
		var revision int64
		a.DB.QueryRow(ctx, "SELECT status,revision FROM wallet_bank_accounts WHERE id=$1", bankID).Scan(&st, &revision)
		if st != "verified" || revision != rev {
			t.Fatalf("unchanged bank reset %s %d", st, revision)
		}
	})
	t.Run("withdrawal idempotency and claim ownership", func(t *testing.T) {
		v := url.Values{"amount": {"200000"}, "request_key": {token()}}
		post(owner, "/wallet/withdrawals", v, 200)
		post(owner, "/wallet/withdrawals", v, 200)
		balance("wallet-owner", 800000, 50000, 200000)
		a.DB.QueryRow(ctx, "SELECT id FROM wallet_withdrawals WHERE request_key=$1", v.Get("request_key")).Scan(&withdrawal)
		post(ops, "/management/wallet/withdrawals/"+withdrawal, url.Values{"action": {"claim"}}, 200)
		post(admin, "/management/wallet/withdrawals/"+withdrawal, url.Values{"action": {"claim"}}, 409)
		post(owner, "/wallet/withdrawals/"+withdrawal+"/cancel", nil, 409)
		post(admin, "/management/wallet/withdrawals/"+withdrawal, url.Values{"action": {"reject"}, "reason": {"نباید آزاد شود"}}, 409)
	})
	payReceipt := func(b *browser, picture []byte, want int) {
		t.Helper()
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		for k, v := range map[string]string{"csrf": b.csrf, "action": "pay", "sender_name": "آزمایش پرداخت‌کننده", "sender_account": "123456789"} {
			mw.WriteField(k, v)
		}
		p, _ := mw.CreateFormFile("receipt", "receipt.png")
		p.Write(picture)
		mw.Close()
		h, c, _ := chatRequest(t, b, http.MethodPost, "/management/wallet/withdrawals/"+withdrawal, mw.FormDataContentType(), &body)
		if c != want {
			t.Fatalf("receipt %d want %d %.300s", c, want, h)
		}
	}
	t.Run("private immutable receipt and payout replay", func(t *testing.T) {
		payReceipt(admin, chatPNG(t), 409)
		payReceipt(ops, []byte("<svg onload=alert(1)>"), 400)
		payReceipt(ops, chatPNG(t), 200)
		balance("wallet-owner", 800000, 50000, 0)
		payReceipt(ops, chatPNG(t), 409)
		for _, b := range []*browser{owner, ops, admin} {
			_, c, headers := chatRequest(t, b, "GET", "/wallet/receipts/"+withdrawal, "", nil)
			if c != 200 || headers.Get("Content-Type") != "image/jpeg" || headers.Get("Cache-Control") != "no-store" {
				t.Fatal("receipt unavailable or cacheable")
			}
		}
		if _, c := other.get("/wallet/receipts/" + withdrawal); c != 404 {
			t.Fatal("receipt leaked")
		}
		if _, e = a.DB.Exec(ctx, "UPDATE wallet_withdrawals SET amount=1 WHERE id=$1", withdrawal); e == nil {
			t.Fatal("paid receipt mutable")
		}
	})
	t.Run("changed bank cancels old approval; freeze blocks spending", func(t *testing.T) {
		post(owner, "/wallet/bank", url.Values{"iban": {"IR762960000000100324200002"}, "holder": {"آزمایش مشتری"}}, 200)
		post(owner, "/wallet/withdrawals", url.Values{"amount": {"100000"}, "request_key": {token()}}, 409)
		post(admin, "/management/wallet/users/wallet-owner/freeze", url.Values{"frozen": {"on"}, "reason": {"بررسی امنیتی موقت"}}, 200)
		post(owner, "/wallet/topup", url.Values{"amount": {"10000"}, "request_key": {token()}}, 409)
		post(admin, "/management/wallet/users/wallet-owner/freeze", url.Values{"reason": {"رفع مسدودی پس از بررسی"}}, 200)
	})
	t.Run("service income and refunds atomic once, promotional credit stays promotional", func(t *testing.T) {
		var session string
		a.DB.QueryRow(ctx, "SELECT token FROM sessions WHERE user_id='wallet-owner' LIMIT 1").Scan(&session)
		id := token()
		_, e = a.DB.Exec(ctx, `INSERT INTO bookings(id,session_token,owner_id,service,provider_id,payment_key,amount,commission_bps,status,payment_status,paid_at) VALUES($1,$2,'wallet-owner','vet','vet-nazanin',$3,1000000,2000,'in_progress','paid',now())`, id, session, token())
		if e != nil {
			t.Fatal(e)
		}
		for i := 0; i < 2; i++ {
			if _, e = a.DB.Exec(ctx, "UPDATE bookings SET status='completed' WHERE id=$1", id); e != nil {
				t.Fatal(e)
			}
		}
		balance("demo-vet", 800000, 0, 0)
		post(admin, "/management/payout/"+id, url.Values{"reference": {"legacy-disabled"}}, 409)
		rid := token()
		_, e = a.DB.Exec(ctx, `INSERT INTO bookings(id,session_token,owner_id,service,payment_key,amount,status,payment_status,refund_amount,refund_status,wallet_bonus_used) VALUES($1,$2,'wallet-owner','vet',$3,100000,'cancel_requested','paid',100000,'pending_refund',30000)`, rid, session, token())
		if e != nil {
			t.Fatal(e)
		}
		post(ops, "/ops/bookings/"+rid, url.Values{"action": {"refund"}}, 200)
		post(ops, "/ops/bookings/"+rid, url.Values{"action": {"refund"}}, 409)
		balance("wallet-owner", 870000, 80000, 0)
		_, e = a.DB.Exec(ctx, "UPDATE bookings SET notes='retry' WHERE id=$1", rid)
		if e != nil {
			t.Fatal(e)
		}
		balance("wallet-owner", 870000, 80000, 0)
	})
	t.Run("checkout mixed wallet and simulated remainder, retry and capacity failure", func(t *testing.T) {
		var session string
		a.DB.QueryRow(ctx, "SELECT token FROM sessions WHERE user_id='wallet-owner' LIMIT 1").Scan(&session)
		slot := token()
		day := time.Now().In(a.Location).AddDate(0, 0, 3).Format("2006-01-02")
		_, e = a.DB.Exec(ctx, "INSERT INTO availability(id,provider_id,day,period,capacity) VALUES($1,'vet-nazanin',$2,'noon',2)", slot, day)
		if e != nil {
			t.Fatal(e)
		}
		id, key := token(), token()
		_, e = a.DB.Exec(ctx, `INSERT INTO bookings(id,session_token,owner_id,service,stage,pet_name,provider_id,availability_id,amount,payment_key) VALUES($1,$2,'wallet-owner','vet',4,'پت','vet-nazanin',$3,350000,$4)`, id, session, slot, key)
		if e != nil {
			t.Fatal(e)
		}
		stampTestQuote(t, a, id)
		if h, c := owner.get("/booking/" + id); c != 200 || !strings.Contains(h, "wallet_amount") {
			t.Fatal("checkout wallet field missing")
		}
		v := url.Values{"payment_key": {key}, "wallet_amount": {"100000"}, "outcome": {"success"}}
		post(owner, "/booking/"+id+"/pay", v, 200)
		post(owner, "/booking/"+id+"/pay", v, 200)
		balance("wallet-owner", 850000, 0, 0)
		var cash, bonus int64
		a.DB.QueryRow(ctx, "SELECT wallet_cash_used,wallet_bonus_used FROM bookings WHERE id=$1", id).Scan(&cash, &bonus)
		if cash != 20000 || bonus != 80000 {
			t.Fatalf("checkout split %d/%d", cash, bonus)
		}
		otherID, otherKey := token(), token()
		_, e = a.DB.Exec(ctx, `INSERT INTO bookings(id,session_token,owner_id,service,stage,pet_name,provider_id,availability_id,amount,payment_key) VALUES($1,$2,'wallet-owner','vet',4,'پت','vet-nazanin',$3,350000,$4)`, otherID, session, slot, otherKey)
		if e != nil {
			t.Fatal(e)
		}
		_, e = a.DB.Exec(ctx, "UPDATE availability SET capacity=1 WHERE id=$1", slot)
		if e != nil {
			t.Fatal(e)
		}
		post(owner, "/booking/"+otherID+"/pay", url.Values{"payment_key": {otherKey}, "wallet_amount": {"100000"}}, 409)
		balance("wallet-owner", 850000, 0, 0)
	})

	t.Run("bonus cannot be withdrawn; changed destination invalidates pending payment", func(t *testing.T) {
		if e = a.GrantReferralReward(ctx, "wallet-other", "other-bonus-only", 500000); e != nil {
			t.Fatal(e)
		}
		post(other, "/wallet/bank", url.Values{"iban": {iban}, "holder": {"مشتری دیگر"}}, 200)
		var bid string
		var revision int64
		a.DB.QueryRow(ctx, "SELECT id,revision FROM wallet_bank_accounts WHERE user_id='wallet-other'").Scan(&bid, &revision)
		post(admin, "/management/wallet/banks/"+bid, url.Values{"status": {"verified"}, "revision": {fmt.Sprint(revision)}, "note": {"تطبیق دستی با نام بانک"}, "attest": {"on"}}, 200)
		post(other, "/wallet/withdrawals", url.Values{"amount": {"100000"}, "request_key": {token()}}, 409)
		balance("wallet-other", 0, 500000, 0)
		a.DB.QueryRow(ctx, "SELECT revision FROM wallet_bank_accounts WHERE id=$1", bankID).Scan(&revision)
		post(admin, "/management/wallet/banks/"+bankID, url.Values{"status": {"verified"}, "revision": {fmt.Sprint(revision)}, "note": {"تطبیق دوباره با نام بانک"}, "attest": {"on"}}, 200)
		k := token()
		post(owner, "/wallet/withdrawals", url.Values{"amount": {"100000"}, "request_key": {k}}, 200)
		var id string
		a.DB.QueryRow(ctx, "SELECT id FROM wallet_withdrawals WHERE request_key=$1", k).Scan(&id)
		post(owner, "/wallet/bank", url.Values{"iban": {iban}, "holder": {"آزمایش مشتری"}}, 200)
		post(ops, "/management/wallet/withdrawals/"+id, url.Values{"action": {"claim"}}, 409)
		post(ops, "/management/wallet/withdrawals/"+id, url.Values{"action": {"reject"}, "reason": {"مقصد نیازمند تأیید دوباره"}}, 200)
		balance("wallet-owner", 850000, 0, 0)
	})
	t.Run("daily cap and pause, historical payouts excluded", func(t *testing.T) {
		post(admin, "/management/wallet/settings", url.Values{"min": {"100000"}, "max": {"200000"}, "daily": {"200000"}, "enabled": {"on"}}, 200)
		var revision int64
		a.DB.QueryRow(ctx, "SELECT revision FROM wallet_bank_accounts WHERE id=$1", bankID).Scan(&revision)
		post(admin, "/management/wallet/banks/"+bankID, url.Values{"status": {"verified"}, "revision": {fmt.Sprint(revision)}, "note": {"تطبیق نهایی با نام بانک"}, "attest": {"on"}}, 200)
		post(owner, "/wallet/withdrawals", url.Values{"amount": {"100000"}, "request_key": {token()}}, 409)
		post(admin, "/management/wallet/settings", url.Values{"min": {"100000"}, "max": {"200000"}, "daily": {"200000"}}, 200)
		post(owner, "/wallet/withdrawals", url.Values{"amount": {"100000"}, "request_key": {token()}}, 409)
		post(admin, "/management/wallet/settings", url.Values{"min": {"100000"}, "max": {"50000000"}, "daily": {"100000000"}, "enabled": {"on"}}, 200)
		var session string
		a.DB.QueryRow(ctx, "SELECT token FROM sessions WHERE user_id='wallet-owner' LIMIT 1").Scan(&session)
		id := token()
		_, e = a.DB.Exec(ctx, `INSERT INTO bookings(id,session_token,owner_id,service,provider_id,payment_key,amount,commission_bps,status,payment_status) VALUES($1,$2,'wallet-owner','vet','vet-nazanin',$3,1000000,2000,'completed','paid')`, id, session, token())
		if e != nil {
			t.Fatal(e)
		}
		_, e = a.DB.Exec(ctx, "INSERT INTO business_payouts(booking_id,provider_id,amount,reference,actor_id) VALUES($1,'vet-nazanin',800000,'old-settlement','demo-admin')", id)
		if e != nil {
			t.Fatal(e)
		}
		_, e = a.DB.Exec(ctx, "UPDATE bookings SET notes='historical payout already recorded' WHERE id=$1", id)
		if e != nil {
			t.Fatal(e)
		}
		balance("demo-vet", 800000, 0, 0)
	})
	t.Run("ledger reconciles to balances", func(t *testing.T) {
		var bad int
		e = a.DB.QueryRow(ctx, `SELECT count(*) FROM wallets w WHERE cash<>(SELECT COALESCE(sum(cash_delta),0) FROM wallet_entries WHERE user_id=w.user_id) OR bonus<>(SELECT COALESCE(sum(bonus_delta),0) FROM wallet_entries WHERE user_id=w.user_id) OR reserved<>(SELECT COALESCE(sum(reserved_delta),0) FROM wallet_entries WHERE user_id=w.user_id)`).Scan(&bad)
		if e != nil || bad != 0 {
			t.Fatalf("wallet reconciliation %d %v", bad, e)
		}
	})
}
