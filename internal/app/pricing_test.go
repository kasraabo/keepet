package app

import (
	"context"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func stampTestQuote(t *testing.T, a *App, id string) PriceQuote {
	t.Helper()
	ctx := context.Background()
	b, e := scanBooking(a.DB.QueryRow(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE id=$1", id))
	if e != nil {
		t.Fatal(e)
	}
	q, e := a.quote(ctx, b)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.DB.Exec(ctx, "UPDATE bookings SET amount=$2,pricing_snapshot=$3 WHERE id=$1", id, q.Total, mustJSON(q)); e != nil {
		t.Fatal(e)
	}
	return q
}
func TestProviderPricing(t *testing.T) {
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
	defer func() { a.DB.Exec(ctx, "TRUNCATE business_config"); a.seedBusiness(ctx) }()
	if _, e = a.DB.Exec(ctx, "TRUNCATE users CASCADE; TRUNCATE business_config"); e != nil {
		t.Fatal(e)
	}
	if e = a.seed(ctx); e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	vet := newBrowser(t, srv)
	vet.login("09120000002")
	groom := newBrowser(t, srv)
	groom.login("09120000003")
	owner := newBrowser(t, srv)
	owner.login("09123456677")
	admin := newBrowser(t, srv)
	admin.login("09120000005")
	post := func(b *browser, path string, v url.Values, want int) {
		t.Helper()
		h, c := b.post(path, v)
		if c != want {
			t.Fatalf("%s got %d want %d %.200s", path, c, want, h)
		}
	}
	post(owner, "/provider/pricing", url.Values{"revision": {"1"}, "hourly_rate": {"1"}}, 403)
	if h, c := vet.get("/provider/pricing"); c != 200 || !strings.Contains(h, "hourly_rate") {
		t.Fatal("vet tariff view", c)
	}
	post(vet, "/provider/pricing", url.Values{"revision": {"1"}, "hourly_rate": {"۱٬۲۰۰٬۰۰۰"}}, 200)
	post(vet, "/provider/pricing", url.Values{"revision": {"1"}, "hourly_rate": {"1"}}, 409)
	post(vet, "/provider/pricing", url.Values{"revision": {"2"}, "hourly_rate": {"-1"}}, 400)
	var rev int64
	a.DB.QueryRow(ctx, "SELECT revision FROM business_config WHERE kind='service' AND id='vet'").Scan(&rev)
	post(admin, "/management/service/vet", url.Values{"revision": {strconv.FormatInt(rev, 10)}, "name": {"ویزیت"}, "commission": {"۲۰۰۰"}, "fixed_fee": {"۵۰٬۰۰۰"}, "visit_minutes": {"۳۰"}, "enabled": {"on"}}, 200)
	q, e := a.quote(ctx, Booking{Service: "vet", ProviderID: "vet-nazanin"})
	if e != nil || q.Base != 600000 || q.Markup != 120000 || q.Fixed != 50000 || q.Total != 770000 || q.Rounding != 0 {
		t.Fatalf("hourly quote %+v %v", q, e)
	}
	// A second specialist's rate is independent.
	other, e := a.quote(ctx, Booking{Service: "vet", ProviderID: "vet-arman"})
	if e != nil || other.Base == q.Base {
		t.Fatal("shared vet rate", other, e)
	}
	ms, e := a.groomTariffs(ctx, "groom-sara")
	if e != nil {
		t.Fatal(e)
	}
	values := url.Values{"revision": {"1"}, "provider_id": {"vet-nazanin"}}
	for _, m := range ms {
		values.Set("price_"+m.ID, "۲۰۰٬۰۰۰")
		values.Set("enabled_"+m.ID, "on")
	}
	values.Del("enabled_ears")
	post(groom, "/provider/pricing", values, 200)
	a.DB.Exec(ctx, `UPDATE business_config SET data=data || '{"commission":1500,"fixed_fee":20000}'::jsonb WHERE kind='service' AND id='grooming'`)
	g, e := a.quote(ctx, Booking{Service: "grooming", ProviderID: "groom-sara", Items: []string{"bath", "nails"}})
	if e != nil || g.Base != 400000 || g.Total != 480000 || g.Rounding != 0 || g.Fixed != 20000 {
		t.Fatal(g, e)
	}
	for _, items := range [][]string{{"ears"}, {"bath", "bath"}, {"unknown"}} {
		if _, e = a.quote(ctx, Booking{Service: "grooming", ProviderID: "groom-sara", Items: items}); e == nil {
			t.Fatal("invalid service accepted", items)
		}
	}
	if _, e = a.quote(ctx, Booking{Service: "grooming", ProviderID: "vet-nazanin", Items: []string{"bath"}}); e == nil {
		t.Fatal("wrong provider service")
	}
	// Different groomers have different customer prices; sorting uses payable totals.
	if _, e = a.DB.Exec(ctx, `INSERT INTO providers(id,name,service,bio,price,active,verified) VALUES('groom-other','گرومر دوم','grooming','تست',1,true,true); INSERT INTO provider_tariffs(provider_id,service_id,price,enabled) SELECT 'groom-other',id,900000,id='bath' FROM business_config WHERE kind='grooming'`); e != nil {
		t.Fatal(e)
	}
	directory, e := a.discover(httptest.NewRequest("GET", "/specialists?service=grooming&sort=price_asc", nil), nil)
	if e != nil || len(directory.Cards) != 2 || directory.Cards[0].ID != "groom-sara" || directory.Cards[0].Cost != 250000 || directory.Cards[1].Cost != 1055000 {
		t.Fatalf("individual customer price order %+v %v", directory, e)
	}
	// Sampling preserves its exact menu price and historical commission model.
	if _, e = a.DB.Exec(ctx, `UPDATE business_config SET data=data || '{"price":123456}'::jsonb WHERE kind='sampling' AND id='blood'; UPDATE business_config SET data=data || '{"commission":2000}'::jsonb WHERE kind='service' AND id='sampling'`); e != nil {
		t.Fatal(e)
	}
	sample, e := a.quote(ctx, Booking{Service: "sampling", Sample: "blood"})
	if e != nil || sample.Total != 123456 || sample.Base != 98765 || sample.Rounding != 0 {
		t.Fatalf("sampling exact price %+v %v", sample, e)
	}
	// Income is the quoted base, even if fees or tariffs change after payment.
	var uid, session string
	a.DB.QueryRow(ctx, "SELECT id FROM users WHERE phone='09123456677'").Scan(&uid)
	a.DB.QueryRow(ctx, "SELECT token FROM sessions WHERE user_id=$1", uid).Scan(&session)
	day := time.Now().In(a.Location).AddDate(0, 0, 3).Format("2006-01-02")
	slot, id, key := token(), token(), token()
	if _, e = a.DB.Exec(ctx, "INSERT INTO availability(id,provider_id,day,period,capacity) VALUES($1,'vet-nazanin',$2,'noon',2)", slot, day); e != nil {
		t.Fatal(e)
	}
	if _, e = a.DB.Exec(ctx, `INSERT INTO bookings(id,session_token,owner_id,service,stage,pet_name,provider_id,availability_id,amount,payment_key) VALUES($1,$2,$3,'vet',4,'پت','vet-nazanin',$4,$5,$6)`, id, session, uid, slot, q.Total, key); e != nil {
		t.Fatal(e)
	}
	stampTestQuote(t, a, id)
	post(owner, "/booking/"+id+"/pay", url.Values{"payment_key": {key}}, 200)
	post(owner, "/booking/"+id+"/pay", url.Values{"payment_key": {key}}, 200)
	post(vet, "/provider/pricing", url.Values{"revision": {"2"}, "hourly_rate": {"۲٬۰۰۰٬۰۰۰"}}, 200)
	if _, e = a.DB.Exec(ctx, "UPDATE bookings SET status='completed' WHERE id=$1", id); e != nil {
		t.Fatal(e)
	}
	var earned int64
	a.DB.QueryRow(ctx, "SELECT cash_delta FROM wallet_entries WHERE idempotency_key=$1", "income:"+id).Scan(&earned)
	if earned != 600000 {
		t.Fatal("provider income must be base", earned)
	}
	if _, e = a.DB.Exec(ctx, "UPDATE bookings SET notes='retry' WHERE id=$1", id); e != nil {
		t.Fatal(e)
	}
	var count int
	a.DB.QueryRow(ctx, "SELECT count(*) FROM wallet_entries WHERE idempotency_key=$1", "income:"+id).Scan(&count)
	if count != 1 {
		t.Fatal("duplicate income")
	}
	if _, err := a.DB.Exec(ctx, "UPDATE bookings SET amount=amount+1 WHERE id=$1", id); err == nil {
		t.Fatal("paid amount was mutable")
	}
	if _, err := a.DB.Exec(ctx, "UPDATE bookings SET provider_due=1 WHERE id=$1", id); err == nil {
		t.Fatal("paid income was mutable")
	}
	// A tariff change must invalidate an unpaid quote.
	post(vet, "/provider/pricing", url.Values{"revision": {"3"}, "hourly_rate": {"۲۰۱۰۰۰۰"}}, 200)
	// An unpaid quote becomes stale when a specialist changes their tariff.
	stale, sk := token(), token()
	a.DB.Exec(ctx, `INSERT INTO bookings(id,session_token,owner_id,service,stage,pet_name,provider_id,availability_id,amount,payment_key) VALUES($1,$2,$3,'vet',4,'پت','vet-nazanin',$4,1,$5)`, stale, session, uid, slot, sk)
	before := stampTestQuote(t, a, stale)
	post(vet, "/provider/pricing", url.Values{"revision": {"4"}, "hourly_rate": {"۲۰۱۰۰۰۱"}}, 200)
	after, e := a.quote(ctx, Booking{Service: "vet", ProviderID: "vet-nazanin"})
	if e != nil || before.Total == after.Total || before.Base == after.Base {
		t.Fatal("tariff change did not change exact price", before, after, e)
	}
	post(owner, "/booking/"+stale+"/pay", url.Values{"payment_key": {sk}}, 409)
}

func TestExactCustomerPrice(t *testing.T) {
	for _, base := range []int64{1, 49999, 50000, 50001, 201234, 770000, 2147483648} {
		q := PriceQuote{Base: base}
		if err := addPriceFees(&q, BusinessData{Commission: 2000, FixedFee: 12345}); err != nil {
			t.Fatal(err)
		}
		commission := (base*2000 + 9999) / 10000
		if q.Base != base || q.Markup != commission || q.Total != base+commission+12345 || q.Rounding != 0 {
			t.Fatalf("unexpected extra charge: %+v", q)
		}
	}
}
