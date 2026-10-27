package app

import (
	"context"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestBookingApproval(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	u, e := url.Parse(dsn)
	if e != nil || !strings.HasSuffix(u.Path, "_test") {
		t.Fatal("test DB required")
	}
	t.Setenv("APP_MODE", "development")
	t.Setenv("DATABASE_URL", dsn)
	ctx := context.Background()
	a, e := New(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := a.DB.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("TRUNCATE users CASCADE;TRUNCATE business_config;TRUNCATE booking_approval_settings;INSERT INTO booking_approval_settings(id) VALUES(1)")
	if e = a.seed(ctx); e != nil {
		t.Fatal(e)
	}
	defer a.DB.Exec(ctx, "UPDATE providers SET requires_ops_approval=false;TRUNCATE booking_approval_settings;INSERT INTO booking_approval_settings(id) VALUES(1)")
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	owner := newBrowser(t, srv)
	owner.login("09128880001")
	admin := newBrowser(t, srv)
	admin.login("09120000005")
	ops := newBrowser(t, srv)
	ops.login("09120000001")
	vet := newBrowser(t, srv)
	vet.login("09120000002")
	groom := newBrowser(t, srv)
	groom.login("09120000003")
	sampler := newBrowser(t, srv)
	sampler.login("09120000004")
	stranger := newBrowser(t, srv)
	stranger.login("09128880002")
	var uid, session string
	a.DB.QueryRow(ctx, "SELECT id FROM users WHERE phone='09128880001'").Scan(&uid)
	a.DB.QueryRow(ctx, "SELECT token FROM sessions WHERE user_id=$1 LIMIT 1", uid).Scan(&session)
	post := func(b *browser, path string, v url.Values, want int) string {
		t.Helper()
		h, c := b.post(path, v)
		if c != want {
			t.Fatalf("%s got %d want %d %.250s", path, c, want, h)
		}
		return h
	}
	state := func(id string) string {
		t.Helper()
		var st string
		if e := a.DB.QueryRow(ctx, "SELECT status FROM bookings WHERE id=$1", id).Scan(&st); e != nil {
			t.Fatal(e)
		}
		return st
	}
	slots := map[string]string{}
	for _, p := range []string{"vet-nazanin", "groom-sara", "sample-amin"} {
		id := token()
		slots[p] = id
		exec("INSERT INTO availability(id,provider_id,day,period,capacity) VALUES($1,$2,(now() AT TIME ZONE 'Asia/Tehran')::date+4,'noon',10)", id, p)
	}
	makeDraft := func(svc, p string) (string, string) {
		t.Helper()
		id, key := token(), token()
		exec(`INSERT INTO bookings(id,session_token,owner_id,service,stage,pet_name,species,age,area,address,provider_id,availability_id,items,sample,answers,payment_key,request_flow) VALUES($1,$2,$3,$4,4,'موچی','cat','دو سال','vanak','نشانی خصوصی برای مراجعه',$5,$6,ARRAY['bath'],'blood','{"symptom":"skin","duration":"today"}',$7,true)`, id, session, uid, svc, p, slots[p], key)
		stampTestQuote(t, a, id)
		return id, key
	}
	action := func(b *browser, id, what string, want int) {
		t.Helper()
		post(b, "/bookings/"+id+"/request-action", url.Values{"action": {what}}, want)
	}
	submit := func(id, key string) {
		t.Helper()
		post(owner, "/booking/"+id+"/request", url.Values{"payment_key": {key}}, 200)
	}
	t.Run("management permissions and optimistic revisions", func(t *testing.T) {
		if _, c := owner.get("/management/booking-policy"); c != 403 {
			t.Fatal(c)
		}
		post(ops, "/management/booking-policy/providers/vet-nazanin", url.Values{"revision": {"1"}, "required": {"on"}}, 403)
		h, c := admin.get("/management/booking-policy")
		if c != 200 || !strings.Contains(h, "provider_minutes") {
			t.Fatal(c)
		}
		post(admin, "/management/booking-policy/providers/vet-nazanin", url.Values{"revision": {"1"}, "required": {"on"}}, 200)
		post(admin, "/management/booking-policy/providers/vet-nazanin", url.Values{"revision": {"1"}}, 409)
		post(admin, "/management/booking-policy/providers/vet-nazanin", url.Values{"revision": {"2"}}, 200)
		post(admin, "/management/booking-policy", url.Values{"revision": {"1"}, "ops_minutes": {"۳۰"}, "provider_minutes": {"۳۰"}, "payment_minutes": {"۱۵"}, "reminder_minutes": {"۱۰"}}, 200)
		post(admin, "/management/booking-policy", url.Values{"revision": {"2"}, "ops_minutes": {"0"}, "provider_minutes": {"30"}, "payment_minutes": {"15"}, "reminder_minutes": {"10"}}, 400)
	})
	t.Run("new drafts opt into request workflow", func(t *testing.T) {
		post(owner, "/book/vet", nil, 200)
		var flag bool
		a.DB.QueryRow(ctx, "SELECT request_flow FROM bookings WHERE owner_id=$1 ORDER BY created_at DESC LIMIT 1", uid).Scan(&flag)
		if !flag {
			t.Fatal("new draft still legacy")
		}
	})
	for _, tc := range []struct {
		svc, p string
		b      *browser
	}{{"vet", "vet-nazanin", vet}, {"grooming", "groom-sara", groom}, {"sampling", "sample-amin", sampler}} {
		t.Run("direct approval and frozen payment "+tc.svc, func(t *testing.T) {
			id, key := makeDraft(tc.svc, tc.p)
			post(owner, "/booking/"+id+"/pay", url.Values{"payment_key": {key}}, 409)
			submit(id, key)
			post(tc.b, "/provider/bookings/"+id, url.Values{"action": {"accept"}}, 409)
			post(ops, "/ops/bookings/"+id, url.Values{"action": {"confirm"}}, 409)
			submit(id, key)
			if state(id) != "pending_provider" {
				t.Fatal(state(id))
			}
			post(owner, "/booking/"+id+"/pay", url.Values{"payment_key": {key}}, 409)
			action(stranger, id, "accept", 403)
			action(owner, id, "accept", 403)
			action(ops, id, "approve", 403)
			h, _ := tc.b.get("/provider")
			if !strings.Contains(h, id) || strings.Contains(h, "نشانی خصوصی برای مراجعه") {
				t.Fatal("pending visibility or address exposure")
			}
			action(tc.b, id, "accept", 200)
			if state(id) != "awaiting_payment" {
				t.Fatal(state(id))
			}
			action(tc.b, id, "accept", 409)
			if _, err := a.DB.Exec(ctx, "UPDATE bookings SET amount=amount+1 WHERE id=$1", id); err == nil {
				t.Fatal("submitted price mutable")
			}
			var amount int64
			a.DB.QueryRow(ctx, "SELECT amount FROM bookings WHERE id=$1", id).Scan(&amount)
			exec("UPDATE providers SET hourly_rate=2000000,tariff_revision=tariff_revision+1 WHERE id=$1 AND service='vet'", tc.p)
			exec("UPDATE business_config SET data=jsonb_set(data,'{commission}','1234') WHERE kind='service' AND id=$1", tc.svc)
			h, c := owner.get("/booking/" + id)
			if c != 200 || strings.Contains(h, "سهم کیپت افزوده") || strings.Contains(h, "دستمزد متخصص</dt>") {
				t.Fatal("payment page exposes components", c)
			}
			post(owner, "/booking/"+id+"/pay", url.Values{"payment_key": {key}}, 200)
			post(owner, "/booking/"+id+"/pay", url.Values{"payment_key": {key}}, 200)
			if state(id) != "accepted" {
				t.Fatal(state(id))
			}
			var after int64
			a.DB.QueryRow(ctx, "SELECT amount FROM bookings WHERE id=$1", id).Scan(&after)
			if amount != after {
				t.Fatal("price changed")
			}
			var n int
			a.DB.QueryRow(ctx, "SELECT count(*) FROM booking_events WHERE booking_id=$1 AND event='paid'", id).Scan(&n)
			if n != 1 {
				t.Fatal("paid replay")
			}
		})
	}
	t.Run("operator gate and request policy snapshot", func(t *testing.T) {
		exec("UPDATE providers SET requires_ops_approval=true WHERE id='vet-nazanin'")
		id, key := makeDraft("vet", "vet-nazanin")
		submit(id, key)
		if state(id) != "pending_ops" {
			t.Fatal(state(id))
		}
		action(vet, id, "accept", 403)
		h, _ := vet.get("/provider")
		if strings.Contains(h, id) {
			t.Fatal("unapproved ops request exposed")
		}
		exec("UPDATE providers SET requires_ops_approval=false WHERE id='vet-nazanin'")
		exec("UPDATE booking_approval_settings SET provider_minutes=5,revision=revision+1")
		action(ops, id, "approve", 200)
		var seconds float64
		a.DB.QueryRow(ctx, "SELECT extract(epoch FROM stage_deadline-now()) FROM bookings WHERE id=$1", id).Scan(&seconds)
		if seconds < 1700 || seconds > 1800 {
			t.Fatal("pending policy overwritten", seconds)
		}
		action(vet, id, "accept", 200)
		exec("UPDATE bookings SET stage_deadline=now()-interval '1 second' WHERE id=$1", id)
		post(owner, "/booking/"+id+"/pay", url.Values{"payment_key": {key}}, 409)
		if e := a.reconcileRequests(ctx); e != nil {
			t.Fatal(e)
		}
		if state(id) != "request_expired" {
			t.Fatal(state(id))
		}
	})
	t.Run("rejection and safe retry preserve intake", func(t *testing.T) {
		id, key := makeDraft("vet", "vet-nazanin")
		submit(id, key)
		action(vet, id, "reject", 200)
		h, _ := owner.get("/bookings/" + id)
		if !strings.Contains(h, "متأسفیم") {
			t.Fatal("no apology")
		}
		action(stranger, id, "retry", 403)
		action(owner, id, "retry", 200)
		var pet, answers, status string
		var provider *string
		a.DB.QueryRow(ctx, "SELECT pet_name,answers::text,status,provider_id FROM bookings WHERE owner_id=$1 AND id<>$2 ORDER BY created_at DESC LIMIT 1", uid, id).Scan(&pet, &answers, &status, &provider)
		if pet != "موچی" || !strings.Contains(answers, "skin") || status != "draft" || provider != nil {
			t.Fatal("retry lost intake or retained provider")
		}
	})
	t.Run("reminders deduplicate and stop at timeout", func(t *testing.T) {
		id, key := makeDraft("vet", "vet-nazanin")
		submit(id, key)
		exec("UPDATE bookings SET provider_requested_at=now()-interval '11 minutes' WHERE id=$1", id)
		for i := 0; i < 2; i++ {
			if e := a.reconcileRequests(ctx); e != nil {
				t.Fatal(e)
			}
		}
		var n int
		a.DB.QueryRow(ctx, "SELECT count(*) FROM outbox WHERE booking_id=$1 AND kind='provider_response_reminder' AND status='local'", id).Scan(&n)
		if n != 1 {
			t.Fatal("duplicate reminder", n)
		}
		exec("UPDATE bookings SET stage_deadline=now()-interval '1 second' WHERE id=$1", id)
		if e := a.reconcileRequests(ctx); e != nil {
			t.Fatal(e)
		}
		if state(id) != "request_expired" {
			t.Fatal(state(id))
		}
		var paid int
		a.DB.QueryRow(ctx, "SELECT count(*) FROM wallet_entries WHERE booking_id=$1", id).Scan(&paid)
		if paid != 0 {
			t.Fatal("unpaid request moved money")
		}
	})
	t.Run("competing provider approvals cannot oversell one capacity", func(t *testing.T) {
		slot := token()
		exec("INSERT INTO availability(id,provider_id,day,period,capacity) VALUES($1,'vet-nazanin',(now() AT TIME ZONE 'Asia/Tehran')::date+5,'noon',1)", slot)
		old := slots["vet-nazanin"]
		slots["vet-nazanin"] = slot
		defer func() { slots["vet-nazanin"] = old }()
		ids := []string{}
		for i := 0; i < 2; i++ {
			id, key := makeDraft("vet", "vet-nazanin")
			submit(id, key)
			ids = append(ids, id)
		}
		var wg sync.WaitGroup
		results := make(chan int, 2)
		for _, id := range ids {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				res, err := vet.client.PostForm(srv.URL+"/bookings/"+id+"/request-action", url.Values{"csrf": {vet.csrf}, "action": {"accept"}})
				if err != nil {
					results <- 0
					return
				}
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
				results <- res.StatusCode
			}(id)
		}
		wg.Wait()
		close(results)
		sum := 0
		for code := range results {
			if code == 200 {
				sum++
			} else if code != 409 {
				t.Fatal(code)
			}
		}
		if sum != 1 {
			t.Fatal("oversold", sum)
		}
		for _, id := range ids {
			if state(id) == "awaiting_payment" {
				exec("UPDATE bookings SET stage_deadline=now()-interval '1 second' WHERE id=$1", id)
			}
		}
		for _, id := range ids {
			if state(id) == "pending_provider" {
				action(vet, id, "accept", 200)
			}
		}
	})
	t.Run("payment cannot confirm an elapsed service window", func(t *testing.T) {
		slot := token()
		exec("INSERT INTO availability(id,provider_id,day,period,capacity) VALUES($1,'groom-sara',(now() AT TIME ZONE 'Asia/Tehran')::date+7,'noon',1)", slot)
		old := slots["groom-sara"]
		slots["groom-sara"] = slot
		defer func() { slots["groom-sara"] = old }()
		id, key := makeDraft("grooming", "groom-sara")
		submit(id, key)
		action(groom, id, "accept", 200)
		exec("UPDATE availability SET day=(now() AT TIME ZONE 'Asia/Tehran')::date-1 WHERE id=$1", slot)
		post(owner, "/booking/"+id+"/pay", url.Values{"payment_key": {key}}, 409)
		var payment string
		a.DB.QueryRow(ctx, "SELECT payment_status FROM bookings WHERE id=$1", id).Scan(&payment)
		if payment != "unpaid" {
			t.Fatal("paid for elapsed window")
		}
	})
	t.Run("wallet is charged only after approval and once", func(t *testing.T) {
		id, key := makeDraft("grooming", "groom-sara")
		exec("SELECT wallet_post($1,'admin_bonus',0,100000,0,$2,NULL,NULL,'اعتبار آزمایشی')", uid, "approval-test-funding")
		submit(id, key)
		post(owner, "/booking/"+id+"/pay", url.Values{"payment_key": {key}, "wallet_amount": {"۵۰٬۰۰۰"}}, 409)
		action(groom, id, "accept", 200)
		for i := 0; i < 2; i++ {
			post(owner, "/booking/"+id+"/pay", url.Values{"payment_key": {key}, "wallet_amount": {"۵۰٬۰۰۰"}}, 200)
		}
		var n int
		var used int64
		a.DB.QueryRow(ctx, "SELECT count(*),COALESCE(-sum(bonus_delta+cash_delta),0) FROM wallet_entries WHERE booking_id=$1 AND kind='booking_payment'", id).Scan(&n, &used)
		if n != 1 || used != 50000 {
			t.Fatal("duplicate or incorrect wallet debit", n, used)
		}
		var deadline *string
		a.DB.QueryRow(ctx, "SELECT stage_deadline::text FROM bookings WHERE id=$1", id).Scan(&deadline)
		if deadline != nil {
			t.Fatal("paid booking still has payment deadline")
		}
	})
	t.Run("unpaid cancellation never creates refund and expired acceptance is blocked", func(t *testing.T) {
		id, key := makeDraft("grooming", "groom-sara")
		submit(id, key)
		action(owner, id, "cancel", 200)
		if state(id) != "request_cancelled" {
			t.Fatal(state(id))
		}
		var refund string
		a.DB.QueryRow(ctx, "SELECT refund_status FROM bookings WHERE id=$1", id).Scan(&refund)
		if refund != "" {
			t.Fatal(refund)
		}
		id, key = makeDraft("grooming", "groom-sara")
		submit(id, key)
		exec("UPDATE bookings SET stage_deadline=now()-interval '1 second' WHERE id=$1", id)
		action(groom, id, "accept", 409)
	})
}
