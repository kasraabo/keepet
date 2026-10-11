package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBusiness(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL test database")
	}
	t.Setenv("APP_MODE", "development")
	t.Setenv("DATABASE_URL", dsn)
	ctx := context.Background()
	a, e := New(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	var db string
	if e = a.DB.QueryRow(ctx, "SELECT current_database()").Scan(&db); e != nil || !strings.HasSuffix(db, "_test") {
		t.Fatal("refusing non-test database")
	}
	if _, e = a.DB.Exec(ctx, "TRUNCATE users CASCADE; TRUNCATE business_config"); e != nil {
		t.Fatal(e)
	}
	if e = a.seed(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() { a.DB.Exec(ctx, "TRUNCATE business_config"); a.seedBusiness(ctx) }()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	admin := newBrowser(t, srv)
	admin.login("09120000005")
	ops := newBrowser(t, srv)
	ops.login("09120000001")
	vet := newBrowser(t, srv)
	vet.login("09120000002")
	groom := newBrowser(t, srv)
	groom.login("09120000003")
	record := func(kind, id string) BusinessRecord {
		t.Helper()
		rs, e := a.records(ctx, kind)
		if e != nil {
			t.Fatal(e)
		}
		for _, r := range rs {
			if r.ID == id {
				return r
			}
		}
		t.Fatalf("record missing %s/%s", kind, id)
		return BusinessRecord{}
	}
	save := func(kind, id string, extras url.Values, want int) {
		t.Helper()
		r := record(kind, id)
		v := url.Values{"revision": {fmtInt(r.Revision)}, "name": {r.Data.Name}, "enabled": {"on"}, "price": {fmtInt(r.Data.Price)}, "commission": {fmtInt(int64(r.Data.Commission))}}
		for k, x := range extras {
			v[k] = x
		}
		html, code := admin.post("/admin/"+kind+"/"+id, v)
		if code != want {
			t.Fatalf("save %s/%s: %d %s", kind, id, code, html)
		}
	}
	t.Run("admin authorization and every view", func(t *testing.T) {
		for _, path := range []string{"/admin", "/admin/catalog", "/admin/supply", "/admin/finance", "/admin/customers", "/admin/triage", "/admin/content", "/admin/audit", "/admin/export.csv"} {
			if _, code := admin.get(path); code != 200 {
				t.Fatalf("%s %d", path, code)
			}
			if _, code := ops.get(path); code != 403 {
				t.Fatalf("ops allowed %s", path)
			}
		}
		if _, code := vet.post("/admin/service/vet", url.Values{"name": {"bad"}}); code != 403 {
			t.Fatal("provider mutation allowed")
		}
	})
	t.Run("live menu audit validation and stale form", func(t *testing.T) {
		r := record("grooming", "bath")
		save("grooming", "bath", url.Values{"price": {"۷۵۰۰۰۰"}}, 200)
		price, e := a.price(ctx, Booking{Service: "grooming", Items: []string{"bath", "nails"}})
		if e != nil || price != 900000 {
			t.Fatalf("price %d %v", price, e)
		}
		if _, code := admin.post("/admin/grooming/bath", url.Values{"revision": {fmtInt(r.Revision)}, "name": {"stale"}, "price": {"1"}, "enabled": {"on"}}); code != 409 {
			t.Fatal("stale write succeeded")
		}
		var count int
		a.DB.QueryRow(ctx, "SELECT count(*) FROM business_audit WHERE kind='grooming' AND target='bath'").Scan(&count)
		if count != 1 {
			t.Fatal("wrong audit count")
		}
		save("grooming", "bath", url.Values{"price": {"-1"}}, 400)
		if record("grooming", "bath").Data.Price != 750000 {
			t.Fatal("invalid write persisted")
		}
		save("grooming", "bath", url.Values{"enabled": {"​"}}, 200)
		if _, e = a.price(ctx, Booking{Service: "grooming", Items: []string{"bath"}}); e == nil {
			t.Fatal("disabled menu priced")
		}
	})
	t.Run("disabled area and service enforce server guards", func(t *testing.T) {
		save("area", "vanak", url.Values{"enabled": {""}}, 200)
		slots, e := a.slots(ctx, Booking{Service: "sampling", Species: "cat", Area: "vanak"})
		if e != nil || len(slots) != 0 {
			t.Fatal("disabled area bookable")
		}
		save("service", "sampling", url.Values{"enabled": {""}}, 200)
		if _, code := vet.post("/book/sampling", nil); code != 404 {
			t.Fatal("disabled service bookable")
		}
		if _, code := vet.get("/services/sampling"); code != 404 {
			t.Fatal("disabled landing")
		}
		save("service", "sampling", nil, 200)
	})
	t.Run("provider deactivation and optimistic lock", func(t *testing.T) {
		ps, e := a.providers(ctx, "vet")
		if e != nil {
			t.Fatal(e)
		}
		p := ps[0]
		original, _ := json.Marshal(map[string]any{"active": p.Active, "price": p.Price, "areas": p.Areas})
		v := url.Values{"original": {string(original)}, "price": {"420000"}, "areas": p.Areas}
		if h, c := admin.post("/admin/provider/"+p.ID, v); c != 200 {
			t.Fatalf("provider %d %s", c, h)
		}
		if _, e := a.price(ctx, Booking{Service: "vet", ProviderID: p.ID}); e == nil {
			t.Fatal("inactive vet priced")
		}
		if _, c := admin.post("/admin/provider/"+p.ID, v); c != 409 {
			t.Fatal("stale provider update")
		}
		original, _ = json.Marshal(map[string]any{"active": false, "price": int64(420000), "areas": p.Areas})
		v.Set("original", string(original))
		v.Set("enabled", "on")
		v.Set("price", fmtInt(p.Price))
		if h, c := admin.post("/admin/provider/"+p.ID, v); c != 200 {
			t.Fatalf("restore %d %s", c, h)
		}
	})
	t.Run("triage validation persistence and consent", func(t *testing.T) {
		qs := a.questions(ctx)
		qs = append(qs, Question{ID: "extra", Label: "پرسش تازه", Type: "textarea", Required: true})
		raw, _ := json.Marshal(qs)
		save("triage", "vet", url.Values{"questions": {string(raw)}}, 200)
		if len(a.questions(ctx)) != len(qs) {
			t.Fatal("triage not live")
		}
		if e = a.seedBusiness(ctx); e != nil {
			t.Fatal(e)
		}
		if len(a.questions(ctx)) != len(qs) {
			t.Fatal("seed overwrote configuration")
		}
		simple := url.Values{"mode": {"simple"}}
		for _, q := range qs {
			simple.Set("label-"+q.ID, q.Label)
			if q.Required {
				simple.Set("required-"+q.ID, "on")
			}
			for _, o := range q.Options {
				simple.Set("option-"+q.ID+"-"+o.Value, o.Label)
			}
		}
		simple.Set("label-extra", "ویرایش سادهٔ پرسش تازه")
		save("triage", "vet", simple, 200)
		live := a.questions(ctx)
		if live[len(live)-1].Label != "ویرایش سادهٔ پرسش تازه" || !live[len(live)-1].Required {
			t.Fatal("simple triage edit failed")
		}
		qs[0].ID = qs[1].ID
		raw, _ = json.Marshal(qs)
		save("triage", "vet", url.Values{"questions": {string(raw)}}, 400)
	})
	t.Run("banner safety public rendering and stored XSS", func(t *testing.T) {
		v := url.Values{"revision": {"0"}, "name": {"بنر آزمایش"}, "body": {"<script>alert(1)</script>"}, "link": {"https://evil.example"}, "enabled": {"on"}}
		if _, c := admin.post("/admin/banner/banner-test", v); c != 400 {
			t.Fatal("external banner allowed")
		}
		v.Set("link", "/services/vet")
		if h, c := admin.post("/admin/banner/banner-test", v); c != 200 {
			t.Fatalf("banner %d %s", c, h)
		}
		anon := newBrowser(t, srv)
		h, c := anon.get("/")
		if c != 200 || !strings.Contains(h, "بنر آزمایش") || strings.Contains(h, "<script>alert(1)</script>") {
			t.Fatal("banner absent or XSS")
		}
	})
	t.Run("scientific review publication and edit invalidation", func(t *testing.T) {
		v := url.Values{"revision": {"0"}, "name": {"مراقبت عمومی"}, "body": {"این یک متن آموزشی آزمایشی است."}}
		if h, c := admin.post("/admin/article/post-test", v); c != 200 {
			t.Fatalf("draft %d %s", c, h)
		}
		save("article", "post-test", url.Values{"body": {v.Get("body")}}, 409)
		if _, c := groom.post("/provider/articles/post-test/review", url.Values{"revision": {"1"}}); c != 403 {
			t.Fatal("non-vet review allowed")
		}
		if h, c := vet.post("/provider/articles/post-test/review", url.Values{"revision": {"1"}}); c != 200 {
			t.Fatalf("review %d %s", c, h)
		}
		save("article", "post-test", url.Values{"body": {v.Get("body")}}, 200)
		anon := newBrowser(t, srv)
		if h, c := anon.get("/magazine/post-test"); c != 200 || !strings.Contains(h, "مراقبت عمومی") {
			t.Fatal("publication missing")
		}
		save("article", "post-test", url.Values{"body": {"متن جدید"}, "enabled": {""}}, 200)
		if record("article", "post-test").Data.ReviewedBy != "" {
			t.Fatal("review not invalidated")
		}
		if _, c := anon.get("/magazine/post-test"); c != 404 {
			t.Fatal("unreviewed article public")
		}
	})
	t.Run("provider availability ownership capacity and customer visibility", func(t *testing.T) {
		day := time.Now().In(a.Location).AddDate(0, 0, 8).Format("2006-01-02")
		v := url.Values{"day": {day}, "period": {"noon"}, "capacity": {"2"}, "provider": {"groom-sara"}}
		h, c := vet.post("/provider/availability", v)
		if c != 200 || !strings.Contains(h, "زمان‌های کاری من") {
			t.Fatalf("vet availability %d %s", c, h)
		}
		var slot string
		var cap int
		e := a.DB.QueryRow(ctx, "SELECT id,capacity FROM availability WHERE provider_id='vet-nazanin' AND day=$1 AND period='noon'", day).Scan(&slot, &cap)
		if e != nil || cap != 2 {
			t.Fatal("vet slot not saved")
		}
		var other int
		a.DB.QueryRow(ctx, "SELECT count(*) FROM availability WHERE provider_id='groom-sara' AND day=$1 AND period='noon'", day).Scan(&other)
		if other != 0 {
			t.Fatal("provider field allowed modifying another provider")
		}
		v.Set("csrf", groom.csrf)
		if h, c = groom.post("/provider/availability", v); c != 200 {
			t.Fatalf("groomer could not save availability %d %s", c, h)
		}
		slots, e := a.slots(ctx, Booking{Service: "vet", Species: "cat", ProviderID: "vet-nazanin"})
		found := false
		for _, x := range slots {
			if x.ID == slot {
				found = true
			}
		}
		if e != nil || !found {
			t.Fatal("provider slot not visible to customer")
		}
		v.Set("csrf", vet.csrf)
		var session string
		a.DB.QueryRow(ctx, "SELECT token FROM sessions WHERE user_id='demo-vet' LIMIT 1").Scan(&session)
		_, e = a.DB.Exec(ctx, "INSERT INTO bookings(id,session_token,owner_id,service,provider_id,availability_id,amount,payment_key,payment_status,status,paid_at) VALUES($1,$2,'demo-vet','vet','vet-nazanin',$3,350000,$4,'paid','paid',now())", token(), session, slot, token())
		if e != nil {
			t.Fatal(e)
		}
		v.Set("capacity", "0")
		if _, c = vet.post("/provider/availability", v); c != 409 {
			t.Fatal("capacity reduced below reservations")
		}
		v.Set("capacity", "1")
		if _, c = vet.post("/provider/availability", v); c != 200 {
			t.Fatal("valid capacity update failed")
		}
		slots, e = a.slots(ctx, Booking{Service: "vet", Species: "cat", ProviderID: "vet-nazanin"})
		for _, x := range slots {
			if x.ID == slot {
				t.Fatal("full slot still bookable")
			}
		}
		v.Set("day", time.Now().In(a.Location).AddDate(0, 0, -1).Format("2006-01-02"))
		if _, c = vet.post("/provider/availability", v); c != 400 {
			t.Fatal("past day accepted")
		}
		v.Set("csrf", ops.csrf)
		if _, c = ops.post("/provider/availability", v); c != 403 {
			t.Fatal("non-provider accepted")
		}
		v.Set("csrf", vet.csrf)
		v.Set("day", day)
		v.Set("period", "night")
		v.Set("capacity", "0")
		if _, c = vet.post("/provider/availability", v); c != 200 {
			t.Fatal("could not close unreserved slot")
		}
	})
	t.Run("commission snapshot actual payment and payout idempotency", func(t *testing.T) {
		save("service", "vet", url.Values{"commission": {"2000"}}, 200)
		var session string
		a.DB.QueryRow(ctx, "SELECT token FROM sessions WHERE user_id='demo-vet' ORDER BY expires_at DESC LIMIT 1").Scan(&session)
		day := time.Now().In(a.Location).AddDate(0, 0, 4).Format("2006-01-02")
		slot, id, key := token(), token(), token()
		if _, e = a.DB.Exec(ctx, "INSERT INTO availability(id,provider_id,day,period,capacity) VALUES($1,'vet-nazanin',$2,'noon',2)", slot, day); e != nil {
			t.Fatal(e)
		}
		if _, e = a.DB.Exec(ctx, "INSERT INTO bookings(id,session_token,owner_id,service,stage,pet_name,provider_id,availability_id,amount,payment_key) VALUES($1,$2,'demo-vet','vet',4,'پت','vet-nazanin',$3,350000,$4)", id, session, slot, key); e != nil {
			t.Fatal(e)
		}
		if h, c := vet.post("/booking/"+id+"/pay", url.Values{"key": {key}, "payment_key": {key}, "result": {"success"}}); c != 200 {
			t.Fatalf("pay %d %s", c, h)
		}
		var bps int
		var paid string
		a.DB.QueryRow(ctx, "SELECT commission_bps,payment_status FROM bookings WHERE id=$1", id).Scan(&bps, &paid)
		if bps != 2000 || paid != "paid" {
			t.Fatalf("snapshot %d %s", bps, paid)
		}
		save("service", "vet", url.Values{"commission": {"5000"}}, 200)
		a.DB.QueryRow(ctx, "SELECT commission_bps FROM bookings WHERE id=$1", id).Scan(&bps)
		if bps != 2000 {
			t.Fatal("retroactive commission")
		}
		if _, c := admin.post("/admin/payout/"+id, url.Values{"reference": {"DEMO-01"}}); c != 409 {
			t.Fatal("uncompleted payout")
		}
		if _, e = a.DB.Exec(ctx, "UPDATE bookings SET status='completed' WHERE id=$1", id); e != nil {
			t.Fatal(e)
		}
		if h, c := admin.post("/admin/payout/"+id, url.Values{"reference": {"=CMD()"}}); c != 200 {
			t.Fatalf("payout %d %s", c, h)
		}
		if _, c := admin.post("/admin/payout/"+id, url.Values{"reference": {"DEMO-02"}}); c != 409 {
			t.Fatal("duplicate payout")
		}
		var amount int64
		a.DB.QueryRow(ctx, "SELECT amount FROM business_payouts WHERE booking_id=$1", id).Scan(&amount)
		if amount != 280000 {
			t.Fatalf("due %d", amount)
		}
		h, c := admin.get("/admin/export.csv")
		if c != 200 || !strings.Contains(h, "'=CMD()") {
			t.Fatal("CSV formula unescaped")
		}
	})
}
func fmtInt(v int64) string { return strconv.FormatInt(v, 10) }
