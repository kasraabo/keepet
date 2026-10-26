package app

import (
	"context"
	"fmt"
	"io"
	assets "keepet"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestReferralProgram(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	u, e := url.Parse(dsn)
	if e != nil || !strings.HasSuffix(u.Path, "_test") {
		t.Fatal("refusing non-test DB")
	}
	t.Setenv("APP_MODE", "development")
	t.Setenv("DATABASE_URL", dsn)
	ctx := context.Background()
	a, e := New(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	defer func() { a.DB.Exec(ctx, "TRUNCATE referral_settings;INSERT INTO referral_settings(id) VALUES(1)") }()
	if _, e = a.DB.Exec(ctx, "TRUNCATE users CASCADE;TRUNCATE business_config;TRUNCATE referral_settings;INSERT INTO referral_settings(id) VALUES(1)"); e != nil {
		t.Fatal(e)
	}
	if e = a.seed(ctx); e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	parent := newBrowser(t, srv)
	parent.login("09126660000")
	admin := newBrowser(t, srv)
	admin.login("09120000005")
	ops := newBrowser(t, srv)
	ops.login("09120000001")
	var parentID string
	a.DB.QueryRow(ctx, "SELECT id FROM users WHERE phone='09126660000'").Scan(&parentID)
	post := func(b *browser, path string, v url.Values, want int) string {
		t.Helper()
		h, c := b.post(path, v)
		if c != want {
			t.Fatalf("%s %d want %d %.220s", path, c, want, h)
		}
		return h
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := a.DB.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	var code ReferralCode
	t.Run("launch terms migrate once and preserve administrator choices", func(t *testing.T) {
		tx, err := a.DB.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		sql, err := assets.Files.ReadFile("migrations/000011_referral_launch_terms.up.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
		var enabled, provider, welcome, signup bool
		var amount, vet, groom, sample, revision int64
		var basis string
		if err = tx.QueryRow(ctx, "SELECT enabled,provider_enabled,invitee_bonus_enabled,referrer_bonus_enabled,invitee_bonus,vet_bps,grooming_bps,sampling_bps,revision,basis FROM referral_settings WHERE id=1").Scan(&enabled, &provider, &welcome, &signup, &amount, &vet, &groom, &sample, &revision, &basis); err != nil {
			t.Fatal(err)
		}
		if !enabled || !provider || !welcome || signup || amount != 200000 || vet != 1000 || groom != 1000 || sample != 1000 || revision != 2 || basis != "platform_share" {
			t.Fatal("incorrect launch terms", amount, vet, groom, sample, revision, basis)
		}
		if _, err = tx.Exec(ctx, "UPDATE referral_settings SET invitee_bonus=123456,revision=revision+1"); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
		if err = tx.QueryRow(ctx, "SELECT invitee_bonus,revision FROM referral_settings WHERE id=1").Scan(&amount, &revision); err != nil || amount != 123456 || revision != 3 {
			t.Fatal("overwrote administrator settings", amount, revision, err)
		}
	})
	t.Run("code allocation, owner access, admin access and disabled defaults", func(t *testing.T) {
		code, e = a.ensureReferralCode(ctx, parentID)
		if e != nil || !referralCodeRE.MatchString(code.Code) {
			t.Fatal(code, e)
		}
		same, e := a.ensureReferralCode(ctx, parentID)
		if e != nil || same.Code != code.Code {
			t.Fatal("unstable referral code", e)
		}
		if h, c := parent.get("/referrals"); c != 200 || !strings.Contains(h, code.Code) {
			t.Fatal("own referral page", c)
		}
		for _, b := range []*browser{parent, ops} {
			if _, c := b.get("/management/referrals"); c != 403 {
				t.Fatal("admin access", c)
			}
			post(b, "/management/referrals/settings", url.Values{}, 403)
		}
		if h, c := admin.get("/management/referrals"); c != 200 || !strings.Contains(h, "referral-settings") {
			t.Fatal("admin referral page", c)
		}
		s, e := a.referralSettings(ctx)
		if e != nil || s.Enabled || s.ReferrerBonus != 0 || s.VetBPS != 0 {
			t.Fatal("unsafe defaults", s, e)
		}
	})
	save := func(bps int, basis string) {
		t.Helper()
		s, e := a.referralSettings(ctx)
		if e != nil {
			t.Fatal(e)
		}
		post(admin, "/management/referrals/settings", url.Values{"revision": {strconv.FormatInt(s.Revision, 10)}, "enabled": {"on"}, "referrer_bonus_enabled": {"on"}, "invitee_bonus_enabled": {"on"}, "provider_enabled": {"on"}, "referrer_bonus": {"۲۰٬۰۰۰"}, "invitee_bonus": {"۳۰٬۰۰۰"}, "vet_bps": {strconv.Itoa(bps)}, "grooming_bps": {strconv.Itoa(bps)}, "sampling_bps": {strconv.Itoa(bps)}, "basis": {basis}}, 200)
	}
	save(2000, "commission")
	var child *browser
	var childID string
	register := func(phone, ref string, tamper bool) (*browser, string) {
		t.Helper()
		b := newBrowser(t, srv)
		h := post(b, "/auth/request", url.Values{"phone": {phone}, "referral_code": {strings.ToLower(ref)}}, 200)
		challenge := field(h, "challenge")
		otp := ""
		match := regexp.MustCompile(`data-dev-code>([0-9]{6})`).FindStringSubmatch(h)
		if len(match) == 2 {
			otp = match[1]
		}
		if challenge == "" || otp == "" {
			t.Fatalf("OTP missing %.200s", h)
		}
		values := url.Values{"challenge": {challenge}, "code": {otp}, "terms": {"on"}, "name": {"عضو معرفی‌شده"}}
		if tamper {
			values.Set("referral_code", "KP-FFFFFFFFFFFF")
		}
		h = post(b, "/auth/verify", values, 200)
		b.csrf = field(h, "csrf")
		b.get("/dashboard")
		var id string
		if e = a.DB.QueryRow(ctx, "SELECT id FROM users WHERE phone=$1", phone).Scan(&id); e != nil {
			t.Fatal(e)
		}
		return b, id
	}
	t.Run("verified signup awards are atomic and cannot be altered at OTP verify", func(t *testing.T) {
		child, childID = register("09126660001", code.Code, true)
		var rid string
		var rb, ib int64
		e = a.DB.QueryRow(ctx, "SELECT referrer_id,referrer_bonus,invitee_bonus FROM referrals WHERE invitee_id=$1", childID).Scan(&rid, &rb, &ib)
		if e != nil || rid != parentID || rb != 20000 || ib != 30000 {
			t.Fatal(rid, rb, ib, e)
		}
		var bonus int64
		a.DB.QueryRow(ctx, "SELECT bonus FROM wallets WHERE user_id=$1", parentID).Scan(&bonus)
		if bonus != 20000 {
			t.Fatal("parent gift", bonus)
		}
		a.DB.QueryRow(ctx, "SELECT bonus FROM wallets WHERE user_id=$1", childID).Scan(&bonus)
		if bonus != 30000 {
			t.Fatal("welcome gift", bonus)
		}
		exec("SELECT referral_register($1,$2)", childID, code.Code)
		a.DB.QueryRow(ctx, "SELECT bonus FROM wallets WHERE user_id=$1", parentID).Scan(&bonus)
		if bonus != 20000 {
			t.Fatal("duplicate signup gift")
		}
		if _, e = a.DB.Exec(ctx, "SELECT referral_register($1,$2)", parentID, code.Code); e == nil {
			t.Fatal("self referral allowed")
		}
		// Neither logging in with a referral code nor a caller-supplied verify field changes an existing account.
		a.DB.Exec(ctx, "UPDATE otp_challenges SET created_at=now()-interval '2 hours' WHERE phone='09126660001'")
		again, _ := register("09126660001", code.Code, false)
		_ = again
		var n int
		a.DB.QueryRow(ctx, "SELECT count(*) FROM referral_events WHERE invitee_id=$1", childID).Scan(&n)
		if n != 2 {
			t.Fatal("existing login rewarded", n)
		}
		if _, e = a.DB.Exec(ctx, "UPDATE referrals SET referrer_id='demo-admin' WHERE invitee_id=$1", childID); e == nil {
			t.Fatal("attribution was mutable")
		}
		if h := post(child, "/auth/request", url.Values{"phone": {"09126660002"}, "referral_code": {"<script>"}}, 200); !strings.Contains(h, "کد معرف معتبر") {
			t.Fatal("invalid code accepted")
		}
	})
	// Promote the same signup through the real application review path.
	t.Run("referred customer becomes approved specialist without losing attribution", func(t *testing.T) {
		ap := token()
		exec("INSERT INTO applications(id,user_id,service,name,license,bio,area,price) VALUES($1,$2,'vet','متخصص معرفی‌شده','TEST-LICENSE','معرفی آزمایشی','vanak',600000)", ap, childID)
		post(ops, "/ops/applications/"+ap, url.Values{"decision": {"approved"}, "verified": {"on"}}, 200)
		var role string
		a.DB.QueryRow(ctx, "SELECT role FROM users WHERE id=$1", childID).Scan(&role)
		if role != "provider" {
			t.Fatal("provider approval failed", role)
		}
	})
	var providerID string
	a.DB.QueryRow(ctx, "SELECT id FROM providers WHERE user_id=$1", childID).Scan(&providerID)
	// Paid bookings use an immutable policy snapshot, the completing specialist's actual referrer and integer toman.
	booking := func(provider, svc string, markup, fixed int64) string {
		t.Helper()
		id := token()
		quote := PriceQuote{Base: 500000, Markup: markup, Fixed: fixed, Total: 500000 + markup + fixed}
		exec(`INSERT INTO bookings(id,session_token,owner_id,service,provider_id,amount,provider_due,pricing_snapshot,payment_key,status) SELECT $1,token,'demo-admin',$2,$3,$4,500000,$5,$6,'accepted' FROM sessions WHERE user_id='demo-admin' LIMIT 1`, id, svc, provider, quote.Total, mustJSON(quote), token())
		exec("UPDATE bookings SET payment_status='paid',paid_at=now() WHERE id=$1", id)
		return id
	}
	complete := func(id string) {
		t.Helper()
		exec("UPDATE bookings SET status=CASE WHEN service='sampling' THEN 'result_ready' ELSE 'completed' END WHERE id=$1", id)
	}
	var original string
	t.Run("commission-only income credited once on completion with the paid rate", func(t *testing.T) {
		original = booking(providerID, "vet", 100000, 50000)
		var amount int64
		a.DB.QueryRow(ctx, "SELECT amount FROM referral_order_awards WHERE booking_id=$1", original).Scan(&amount)
		if amount != 20000 {
			t.Fatal("fixed fee included", amount)
		}
		var cash int64
		a.DB.QueryRow(ctx, "SELECT COALESCE((SELECT cash FROM wallets WHERE user_id=$1),0)", parentID).Scan(&cash)
		if cash != 0 {
			t.Fatal("income paid before completion", cash)
		}
		save(8000, "platform_share")
		complete(original)
		exec("UPDATE bookings SET notes='retry' WHERE id=$1", original)
		a.DB.QueryRow(ctx, "SELECT cash FROM wallets WHERE user_id=$1", parentID).Scan(&cash)
		if cash != 20000 {
			t.Fatal("snapshot not honored", cash)
		}
		var providerIncome int64
		a.DB.QueryRow(ctx, "SELECT cash_delta FROM wallet_entries WHERE idempotency_key=$1", "income:"+original).Scan(&providerIncome)
		if providerIncome != 500000 {
			t.Fatal("referral deducted provider income", providerIncome)
		}
		var n int
		a.DB.QueryRow(ctx, "SELECT count(*) FROM referral_events WHERE booking_id=$1 AND kind='order_income'", original).Scan(&n)
		if n != 1 {
			t.Fatal("duplicate income", n)
		}
		if _, e = a.DB.Exec(ctx, "UPDATE referral_order_awards SET amount=1 WHERE booking_id=$1", original); e == nil {
			t.Fatal("credited award mutable")
		}
	})
	t.Run("cash-income policy includes fixed fee only when configured", func(t *testing.T) {
		id := booking(providerID, "vet", 100000, 50000)
		complete(id)
		var amt int64
		a.DB.QueryRow(ctx, "SELECT amount FROM referral_order_awards WHERE booking_id=$1", id).Scan(&amt)
		if amt != 120000 {
			t.Fatal("platform share basis", amt)
		}
		report, e := a.loadReport(ctx, httptest.NewRequest("GET", "/management/reports", nil))
		if e != nil || report.ReferralCost != 140000 {
			t.Fatal("referral expense report", report, e)
		}
	})
	t.Run("refund cancels pending reward and reverses credited income without negative wallets", func(t *testing.T) {
		cancelled := booking(providerID, "vet", 100000, 50000)
		exec("UPDATE bookings SET refund_status='refunded',refund_amount=amount,status='cancelled' WHERE id=$1", cancelled)
		var status string
		a.DB.QueryRow(ctx, "SELECT status FROM referral_order_awards WHERE booking_id=$1", cancelled).Scan(&status)
		if status != "cancelled" {
			t.Fatal("cancelled reward", status)
		}
		// Simulate already-withdrawn referral income with a valid ledger entry.
		exec("SELECT wallet_post($1,'withdrawal_paid',-140000,0,0,'test-referral-withdrawn',NULL,NULL,'برداشت آزمایشی')", parentID)
		exec("UPDATE bookings SET refund_status='refunded',refund_amount=amount WHERE id=$1", original)
		var debt, cash int64
		a.DB.QueryRow(ctx, "SELECT debt FROM referral_accounts WHERE user_id=$1", parentID).Scan(&debt)
		a.DB.QueryRow(ctx, "SELECT cash FROM wallets WHERE user_id=$1", parentID).Scan(&cash)
		if debt != 20000 || cash != 0 {
			t.Fatal("negative wallet or missing debt", debt, cash)
		}
		exec("UPDATE bookings SET notes='refund retry' WHERE id=$1", original)
		a.DB.QueryRow(ctx, "SELECT debt FROM referral_accounts WHERE user_id=$1", parentID).Scan(&debt)
		if debt != 20000 {
			t.Fatal("duplicate reversal", debt)
		}
		exec("SELECT wallet_post($1,'admin_cash',15000,0,0,'referral-recovery-partial',NULL,NULL,'اعتبار نقدی آزمایشی')", parentID)
		a.DB.QueryRow(ctx, "SELECT debt FROM referral_accounts WHERE user_id=$1", parentID).Scan(&debt)
		if debt != 5000 {
			t.Fatal("partial recovery", debt)
		}
		exec("SELECT wallet_post($1,'admin_cash',10000,0,0,'referral-recovery-final',NULL,NULL,'اعتبار نقدی آزمایشی')", parentID)
		a.DB.QueryRow(ctx, "SELECT debt FROM referral_accounts WHERE user_id=$1", parentID).Scan(&debt)
		a.DB.QueryRow(ctx, "SELECT cash FROM wallets WHERE user_id=$1", parentID).Scan(&cash)
		if debt != 0 || cash != 5000 {
			t.Fatal("debt recovery", debt, cash)
		}
		var gift int64
		a.DB.QueryRow(ctx, "SELECT bonus FROM wallets WHERE user_id=$1", parentID).Scan(&gift)
		if gift != 20000 {
			t.Fatal("reversal touched gift", gift)
		}
	})
	t.Run("referral code and individual pause affect future orders, not paid snapshots", func(t *testing.T) {
		before := booking(providerID, "vet", 100000, 0)
		var rev int64
		a.DB.QueryRow(ctx, "SELECT revision FROM referrals WHERE invitee_id=$1", childID).Scan(&rev)
		post(admin, "/management/referrals/person/"+childID, url.Values{"revision": {fmt.Sprint(rev)}}, 200)
		after := booking(providerID, "vet", 100000, 0)
		complete(before)
		complete(after)
		var count int
		a.DB.QueryRow(ctx, "SELECT count(*) FROM referral_events WHERE booking_id=$1", after).Scan(&count)
		if count != 0 {
			t.Fatal("paused referral awarded")
		}
		a.DB.QueryRow(ctx, "SELECT count(*) FROM referral_events WHERE booking_id=$1", before).Scan(&count)
		if count != 1 {
			t.Fatal("paid snapshot lost")
		}
		exec("UPDATE referrals SET enabled=true,revision=revision+1 WHERE invitee_id=$1", childID)
		a.DB.QueryRow(ctx, "SELECT revision FROM referral_codes WHERE user_id=$1", parentID).Scan(&rev)
		post(admin, "/management/referrals/code/"+parentID, url.Values{"revision": {fmt.Sprint(rev)}}, 200)
		if h := post(parent, "/auth/request", url.Values{"phone": {"09126660002"}, "referral_code": {code.Code}}, 200); !strings.Contains(h, "کد معرف معتبر") {
			t.Fatal("disabled code accepted")
		}
		blocked := booking(providerID, "vet", 100000, 0)
		complete(blocked)
		a.DB.QueryRow(ctx, "SELECT count(*) FROM referral_events WHERE booking_id=$1", blocked).Scan(&count)
		if count != 0 {
			t.Fatal("disabled code accrued income")
		}
		exec("UPDATE referral_codes SET enabled=true,revision=revision+1 WHERE user_id=$1", parentID)
	})
	t.Run("actual replacement provider receives attribution, with the original paid policy", func(t *testing.T) {
		unrelated := booking("vet-nazanin", "vet", 100000, 0)
		exec("UPDATE bookings SET provider_id=$2 WHERE id=$1", unrelated, providerID)
		complete(unrelated)
		var rid string
		a.DB.QueryRow(ctx, "SELECT referrer_id FROM referral_order_awards WHERE booking_id=$1", unrelated).Scan(&rid)
		if rid != parentID {
			t.Fatal("replacement attribution missing")
		}
		replaced := booking(providerID, "vet", 100000, 0)
		exec("UPDATE bookings SET provider_id='vet-nazanin' WHERE id=$1", replaced)
		complete(replaced)
		var n int
		a.DB.QueryRow(ctx, "SELECT count(*) FROM referral_events WHERE booking_id=$1", replaced).Scan(&n)
		if n != 0 {
			t.Fatal("old provider referrer paid after replacement")
		}
	})
	t.Run("sampling and grooming completion rules and simultaneous replay", func(t *testing.T) {
		for index, svc := range []string{"grooming", "sampling"} {
			pid := token()
			exec("INSERT INTO users(id,phone,role,name) VALUES($1,$2,'provider','همکار آزمایشی')", pid, fmt.Sprintf("0912777000%d", index))
			exec("SELECT referral_register($1,$2)", pid, code.Code)
			pr := token()
			exec("INSERT INTO providers(id,user_id,name,service,bio,price,verified,active) VALUES($1,$2,'همکار آزمایشی',$3,'تست',1,true,true)", pr, pid, svc)
			id := booking(pr, svc, 100000, 0)
			if svc == "sampling" {
				exec("UPDATE bookings SET status='delivered' WHERE id=$1", id)
				exec("UPDATE bookings SET status='completed' WHERE id=$1", id)
				var n int
				a.DB.QueryRow(ctx, "SELECT count(*) FROM referral_events WHERE booking_id=$1", id).Scan(&n)
				if n != 0 {
					t.Fatal("sampling paid before result")
				}
			}
			var wg sync.WaitGroup
			errs := make(chan error, 4)
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := a.DB.Exec(ctx, "UPDATE bookings SET status=CASE WHEN service='sampling' THEN 'result_ready' ELSE 'completed' END WHERE id=$1", id)
					errs <- err
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			var n int
			a.DB.QueryRow(ctx, "SELECT count(*) FROM referral_events WHERE booking_id=$1 AND kind='order_income'", id).Scan(&n)
			if n != 1 {
				t.Fatal("concurrent duplicate income", svc, n)
			}
		}
	})
	t.Run("history ownership, escaping, settings stale forms, CSRF and ledger immutability", func(t *testing.T) {
		exec("UPDATE users SET name='<script>private-referral-name</script>' WHERE id=$1", childID)
		h, c := parent.get("/referrals?q=09120000005")
		if c != 200 || strings.Contains(h, "<script>private-referral-name") || !strings.Contains(h, "&lt;script&gt;private-referral-name") || strings.Contains(h, "09126660001") {
			t.Fatal("history privacy or escaping", c)
		}
		other := newBrowser(t, srv)
		other.login("09126660009")
		h, c = other.get("/referrals?user_id=" + parentID)
		if c != 200 || strings.Contains(h, "private-referral-name") {
			t.Fatal("cross-account referral access", c)
		}
		s, _ := a.referralSettings(ctx)
		save(1000, "commission")
		post(admin, "/management/referrals/settings", url.Values{"revision": {fmt.Sprint(s.Revision)}, "basis": {"commission"}, "referrer_bonus": {"0"}, "invitee_bonus": {"0"}, "vet_bps": {"0"}, "grooming_bps": {"0"}, "sampling_bps": {"0"}}, 409)
		post(admin, "/management/referrals/code/"+parentID, url.Values{"csrf": {"bad"}, "revision": {"1"}}, 403)
		if _, e = a.DB.Exec(ctx, "UPDATE referral_events SET amount=1"); e == nil {
			t.Fatal("referral events mutable")
		}
		if _, e = a.DB.Exec(ctx, "UPDATE referral_booking_policy SET rate_bps=1"); e == nil {
			t.Fatal("paid policy mutable")
		}
	})
	t.Run("partial refunds retain only proportional earned commission", func(t *testing.T) {
		save(2000, "commission")
		id := booking(providerID, "vet", 100000, 0)
		complete(id)
		exec("UPDATE bookings SET refund_status='refunded',refund_amount=150000 WHERE id=$1", id)
		var adjusted int64
		a.DB.QueryRow(ctx, "SELECT adjusted FROM referral_order_awards WHERE booking_id=$1", id).Scan(&adjusted)
		if adjusted != 5000 {
			t.Fatal("partial refund commission", adjusted)
		}
		exec("UPDATE bookings SET notes='partial retry' WHERE id=$1", id)
		var n int
		a.DB.QueryRow(ctx, "SELECT count(*) FROM referral_events WHERE booking_id=$1 AND kind='order_reversal'", id).Scan(&n)
		if n != 1 {
			t.Fatal("partial refund replay")
		}
	})
	t.Run("disabled program records attribution without paying or retroactively rewarding", func(t *testing.T) {
		exec("UPDATE referral_settings SET enabled=false,revision=revision+1 WHERE id=1")
		_, offID := register("09126660008", code.Code, false)
		var rb, ib int64
		a.DB.QueryRow(ctx, "SELECT referrer_bonus,invitee_bonus FROM referrals WHERE invitee_id=$1", offID).Scan(&rb, &ib)
		if rb != 0 || ib != 0 {
			t.Fatal("disabled signup paid")
		}
		id := booking(providerID, "vet", 100000, 0)
		save(2000, "commission")
		complete(id)
		var count int
		a.DB.QueryRow(ctx, "SELECT count(*) FROM referral_events WHERE booking_id=$1", id).Scan(&count)
		if count != 0 {
			t.Fatal("disabled order retroactively rewarded")
		}
	})
	t.Run("old paid orders do not gain retroactive awards after provider replacement", func(t *testing.T) {
		save(2000, "commission")
		id := token()
		exec(`INSERT INTO bookings(id,session_token,owner_id,service,provider_id,amount,provider_due,payment_key,status,payment_status,paid_at) SELECT $1,token,'demo-admin','vet','vet-nazanin',600000,500000,$2,'accepted','paid',now() FROM sessions WHERE user_id='demo-admin' LIMIT 1`, id, token())
		exec("UPDATE bookings SET provider_id=$2 WHERE id=$1", id, providerID)
		complete(id)
		var n int
		if err := a.DB.QueryRow(ctx, "SELECT count(*) FROM referral_order_awards WHERE booking_id=$1", id).Scan(&n); err != nil || n != 0 {
			t.Fatal("legacy order rewarded retroactively", n, err)
		}
	})
	t.Run("concurrent verified signups with two codes have one fixed attribution", func(t *testing.T) {
		alt, e := a.ensureReferralCode(ctx, "demo-vet2")
		if e != nil {
			t.Fatal(e)
		}
		phone := "09126660007"
		first, second := newBrowser(t, srv), newBrowser(t, srv)
		h1 := post(first, "/auth/request", url.Values{"phone": {phone}, "referral_code": {code.Code}}, 200)
		exec("UPDATE otp_challenges SET created_at=now()-interval '2 hours' WHERE phone=$1", phone)
		h2 := post(second, "/auth/request", url.Values{"phone": {phone}, "referral_code": {alt.Code}}, 200)
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for i, b := range []*browser{first, second} {
			html := []string{h1, h2}[i]
			m := regexp.MustCompile(`data-dev-code>([0-9]{6})`).FindStringSubmatch(html)
			if len(m) != 2 {
				t.Fatal("OTP missing")
			}
			vals := url.Values{"csrf": {b.csrf}, "challenge": {field(html, "challenge")}, "code": {m[1]}, "terms": {"on"}, "name": {"عضو هم‌زمان"}}
			wg.Add(1)
			go func(b *browser, v url.Values) {
				defer wg.Done()
				res, err := b.client.PostForm(srv.URL+"/auth/verify", v)
				if err == nil {
					io.Copy(io.Discard, res.Body)
					res.Body.Close()
					if res.StatusCode != 200 {
						err = fmt.Errorf("concurrent signup status %d", res.StatusCode)
					}
				}
				results <- err
			}(b, vals)
		}
		wg.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatal(err)
			}
		}
		var count int
		a.DB.QueryRow(ctx, "SELECT count(*) FROM referrals r JOIN users u ON u.id=r.invitee_id WHERE u.phone=$1", phone).Scan(&count)
		if count != 1 {
			t.Fatal("duplicate referral attribution")
		}
		a.DB.QueryRow(ctx, "SELECT count(*) FROM referral_events e JOIN users u ON u.id=e.invitee_id WHERE u.phone=$1", phone).Scan(&count)
		if count != 2 {
			t.Fatal("concurrent duplicate welcome or referrer bonus", count)
		}
	})

}
