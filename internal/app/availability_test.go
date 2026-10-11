package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests use the real forms and middleware. A slot's capacity, identity and
// time window must remain valid even when the operator submits a stale page.
func TestCustomProviderAvailability(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL test database")
	}
	t.Setenv("APP_MODE", "development")
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("ADMIN_SETTINGS_KEY", "")
	t.Setenv("SETTINGS_KEY_FILE", filepath.Join(t.TempDir(), "settings.key"))
	ctx := context.Background()
	a, err := New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var dbname string
	if err = a.DB.QueryRow(ctx, "SELECT current_database()").Scan(&dbname); err != nil || !strings.HasSuffix(dbname, "_test") {
		t.Fatal("refusing to reset a database not suffixed _test")
	}
	if _, err = a.DB.Exec(ctx, "TRUNCATE users CASCADE; TRUNCATE business_config"); err != nil {
		t.Fatal(err)
	}
	if err = a.seed(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(ctx, `UPDATE platform_integrations SET settings='{"digits":"6","ttl":"120","resend":"30","hourly":"5","attempts":"5"}',secret=NULL,revision=1 WHERE id='otp'`); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	owner := newBrowser(t, srv)
	owner.login("09128882221")
	vet := newBrowser(t, srv)
	vet.login("09120000002")
	vet2 := newBrowser(t, srv)
	vet2.login("09120000006")
	groomer := newBrowser(t, srv)
	groomer.login("09120000003")
	admin := newBrowser(t, srv)
	admin.login("09120000005")
	var ownerID, session string
	if err = a.DB.QueryRow(ctx, "SELECT id FROM users WHERE phone='09128882221'").Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	cookieURL, _ := url.Parse(srv.URL)
	for _, c := range owner.client.Jar.Cookies(cookieURL) {
		if c.Name == "keepet_session" {
			session = c.Value
		}
	}
	if session == "" {
		t.Fatal("missing owner session")
	}
	day := func(delta int) string {
		return time.Now().In(a.Location).AddDate(0, 0, delta).Format("2006-01-02")
	}
	save := func(b *browser, date, from, to, capacity string) (string, int) {
		return b.post("/provider/availability", url.Values{"day": {date}, "period": {"custom"}, "start_time": {from}, "end_time": {to}, "capacity": {capacity}})
	}
	findSlot := func(provider, date, from, to string) string {
		t.Helper()
		var id string
		if e := a.DB.QueryRow(ctx, `SELECT id FROM availability WHERE provider_id=$1 AND day=$2 AND start_time=$3::time AND end_time=$4::time`, provider, date, from, to).Scan(&id); e != nil {
			t.Fatal(e)
		}
		return id
	}
	createSlot := func(b *browser, provider, date, from, to, capacity string) string {
		t.Helper()
		if body, code := save(b, date, from, to, capacity); code != 200 {
			t.Fatalf("save custom availability %d: %s", code, body)
		}
		return findSlot(provider, date, from, to)
	}
	fixture := func(slot, provider, svc, species, area, status, payment string) string {
		t.Helper()
		id := token()
		_, e := a.DB.Exec(ctx, `INSERT INTO bookings(id,session_token,owner_id,service,stage,pet_name,species,area,address,provider_id,availability_id,amount,payment_key,status,payment_status,paid_at,consent) VALUES($1,$2,$3,$4,4,$5,$6,$7,'تهران آدرس آزمایشی',NULLIF($8,''),NULLIF($9,''),350000,$10,$11,$12,CASE WHEN $12='paid' THEN now() ELSE NULL END,true)`, id, session, ownerID, svc, "زمان سفارشی "+id[:8], species, area, provider, slot, token(), status, payment)
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	assign := func(bid, slot, clock string) (string, int) {
		return admin.post("/management/operations/bookings/"+bid, url.Values{"action": {"assign"}, "availability": {slot}, "exact_clock": {clock}})
	}
	assertStatus := func(bid, want string) {
		t.Helper()
		var got string
		if e := a.DB.QueryRow(ctx, "SELECT status FROM bookings WHERE id=$1", bid).Scan(&got); e != nil {
			t.Fatal(e)
		}
		if got != want {
			t.Fatalf("booking status %s want %s", got, want)
		}
	}
	assignmentForm := func(body, bid string) string {
		t.Helper()
		forms := regexp.MustCompile(`(?s)<form[^>]*action="/management/operations/bookings/`+regexp.QuoteMeta(bid)+`"[^>]*>.*?</form>`).FindAllString(body, -1)
		for _, form := range forms {
			if strings.Contains(form, `value="assign"`) {
				return form
			}
		}
		t.Fatal("missing assignment form for booking " + bid)
		return ""
	}

	var earlySlot, lateSlot string
	t.Run("multiple custom ranges and idempotent own-provider update", func(t *testing.T) {
		earlySlot = createSlot(vet, "vet-nazanin", day(1), "07:15", "08:45", "2")
		lateSlot = createSlot(vet, "vet-nazanin", day(1), "21:20", "22:10", "2")
		if earlySlot == lateSlot {
			t.Fatal("distinct custom time ranges collapsed to one slot")
		}
		body, code := vet.post("/provider/availability", url.Values{"provider": {"vet-arman"}, "day": {day(1)}, "period": {"custom"}, "start_time": {"07:15"}, "end_time": {"08:45"}, "capacity": {"4"}})
		if code != 200 {
			t.Fatalf("custom range update %d: %s", code, body)
		}
		var n, capacity int
		if e := a.DB.QueryRow(ctx, `SELECT count(*) FROM availability WHERE provider_id='vet-nazanin' AND day=$1 AND period='custom'`, day(1)).Scan(&n); e != nil || n != 2 {
			t.Fatalf("duplicate custom slots: count=%d err=%v", n, e)
		}
		if e := a.DB.QueryRow(ctx, "SELECT capacity FROM availability WHERE id=$1", earlySlot).Scan(&capacity); e != nil || capacity != 4 {
			t.Fatalf("capacity update=%d err=%v", capacity, e)
		}
		if e := a.DB.QueryRow(ctx, `SELECT count(*) FROM availability WHERE provider_id='vet-arman' AND day=$1 AND period='custom'`, day(1)).Scan(&n); e != nil || n != 0 {
			t.Fatalf("provider field changed another account: count=%d err=%v", n, e)
		}
		if body, code = vet.get("/provider"); code != 200 || !strings.Contains(normalize(body), "07:15") || !strings.Contains(normalize(body), "22:10") {
			t.Fatalf("custom clocks missing in provider panel %d: %s", code, body)
		}
	})

	t.Run("invalid custom ranges and capacity cannot create a slot", func(t *testing.T) {
		for _, tc := range []struct{ name, date, from, to, cap string }{
			{"reversed", day(2), "08:45", "07:15", "1"},
			{"empty interval", day(2), "07:15", "07:15", "1"},
			{"invalid start", day(2), "25:10", "08:45", "1"},
			{"missing end", day(2), "07:15", "", "1"},
			{"past day", day(-1), "07:15", "08:45", "1"},
			{"too far ahead", day(91), "07:15", "08:45", "1"},
			{"negative capacity", day(2), "07:15", "08:45", "-1"},
			{"over capacity limit", day(2), "07:15", "08:45", "21"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				body, code := save(vet, tc.date, tc.from, tc.to, tc.cap)
				if code != 400 {
					t.Fatalf("invalid custom range status %d: %s", code, body)
				}
			})
		}
		local := time.Now().In(a.Location)
		if local.Hour() > 0 || local.Minute() > 3 {
			end := local.Add(-2 * time.Minute).Format("15:04")
			if body, code := save(vet, day(0), "00:00", end, "1"); code != 400 {
				t.Fatalf("ended custom range status %d: %s", code, body)
			}
		}
		if body, code := owner.post("/provider/availability", url.Values{"day": {day(2)}, "period": {"custom"}, "start_time": {"07:15"}, "end_time": {"08:45"}, "capacity": {"1"}}); code != 403 {
			t.Fatalf("owner custom availability status %d: %s", code, body)
		}
	})

	t.Run("preset compatibility and legacy database inserts get clocks", func(t *testing.T) {
		if body, code := vet.post("/provider/availability", url.Values{"day": {day(2)}, "period": {"morning"}, "capacity": {"3"}}); code != 200 {
			t.Fatalf("preset API status %d: %s", code, body)
		}
		var from, to string
		if e := a.DB.QueryRow(ctx, `SELECT start_time::text,end_time::text FROM availability WHERE provider_id='vet-nazanin' AND day=$1 AND period='morning'`, day(2)).Scan(&from, &to); e != nil || from != "09:00:00" || to != "12:00:00" {
			t.Fatalf("preset window %s-%s err=%v", from, to, e)
		}
		id := token()
		if _, e := a.DB.Exec(ctx, `INSERT INTO availability(id,provider_id,day,period,capacity) VALUES($1,'vet-nazanin',$2,'noon',1)`, id, day(2)); e != nil {
			t.Fatal(e)
		}
		if e := a.DB.QueryRow(ctx, "SELECT start_time::text,end_time::text FROM availability WHERE id=$1", id).Scan(&from, &to); e != nil || from != "12:00:00" || to != "15:00:00" {
			t.Fatalf("legacy insert window %s-%s err=%v", from, to, e)
		}
	})

	t.Run("custom slot is selectable and payable through customer flow", func(t *testing.T) {
		bid := fixture("", "vet-nazanin", "vet", "cat", "", "draft", "unpaid")
		if _, e := a.DB.Exec(ctx, "UPDATE bookings SET stage=3 WHERE id=$1", bid); e != nil {
			t.Fatal(e)
		}
		body, code := owner.get("/booking/" + bid)
		if code != 200 || !strings.Contains(body, earlySlot) || !strings.Contains(normalize(body), "07:15") || !strings.Contains(normalize(body), "08:45") {
			t.Fatalf("customer custom slot missing %d: %s", code, body)
		}
		body, code = owner.post("/booking/"+bid+"/step", url.Values{"step": {"3"}, "slot": {earlySlot}})
		if code != 200 {
			t.Fatalf("select custom slot %d: %s", code, body)
		}
		key := field(body, "payment_key")
		if key == "" {
			t.Fatal("custom slot did not advance to payment")
		}
		body, code = owner.post("/booking/"+bid+"/pay", url.Values{"payment_key": {key}, "outcome": {"success"}})
		if code != 200 {
			t.Fatalf("pay custom slot %d: %s", code, body)
		}
		assertStatus(bid, "paid")
	})

	t.Run("booked capacity remains available to its own assignment and provider gets job", func(t *testing.T) {
		slot := createSlot(vet, "vet-nazanin", day(3), "07:15", "08:45", "1")
		bid := fixture(slot, "vet-nazanin", "vet", "cat", "", "confirmed", "paid")
		if body, code := vet.get("/provider"); code != 200 || strings.Contains(body, "/provider/bookings/"+bid) {
			t.Fatalf("confirmed unassigned booking appeared as a provider job %d", code)
		}
		body, code := admin.get("/management/operations")
		if code != 200 || !strings.Contains(assignmentForm(body, bid), slot) {
			t.Fatalf("own reserved capacity omitted from assignment %d: %s", code, body)
		}
		body, code = assign(bid, slot, "07:40")
		if code != 200 {
			t.Fatalf("assign own reserved capacity %d: %s", code, body)
		}
		assertStatus(bid, "assigned")
		var provider, gotSlot string
		var exact time.Time
		if e := a.DB.QueryRow(ctx, "SELECT provider_id,availability_id,exact_time FROM bookings WHERE id=$1", bid).Scan(&provider, &gotSlot, &exact); e != nil {
			t.Fatal(e)
		}
		if provider != "vet-nazanin" || gotSlot != slot || exact.In(a.Location).Format("2006-01-02T15:04") != day(3)+"T07:40" {
			t.Fatalf("assignment not derived from slot: %s %s %s", provider, gotSlot, exact)
		}
		if body, code = vet.get("/provider"); code != 200 || !strings.Contains(body, "/provider/bookings/"+bid) || !strings.Contains(body, "پذیرش کار") {
			t.Fatalf("assigned booking missing from provider jobs %d: %s", code, body)
		}
		if body, code = vet2.get("/provider"); code != 200 || strings.Contains(body, "/provider/bookings/"+bid) {
			t.Fatalf("assigned booking exposed to another provider %d", code)
		}
		if body, code = save(vet, day(3), "07:15", "08:45", "0"); code != 409 {
			t.Fatalf("capacity below booked count %d: %s", code, body)
		}
		var capacity int
		if e := a.DB.QueryRow(ctx, "SELECT capacity FROM availability WHERE id=$1", slot).Scan(&capacity); e != nil || capacity != 1 {
			t.Fatalf("failed capacity reduction changed slot: %d err=%v", capacity, e)
		}
	})

	t.Run("clock and selected vet are checked against chosen slot", func(t *testing.T) {
		slot := createSlot(vet, "vet-nazanin", day(4), "07:15", "08:45", "4")
		bid := fixture("", "vet-nazanin", "vet", "cat", "", "confirmed", "paid")
		for _, clock := range []string{"07:14", "08:45", "09:00", "invalid"} {
			if body, code := assign(bid, slot, clock); code != 400 && code != 409 {
				t.Fatalf("outside or invalid exact clock %s status %d: %s", clock, code, body)
			}
			assertStatus(bid, "confirmed")
		}
		otherSlot := createSlot(vet2, "vet-arman", day(4), "07:15", "08:45", "2")
		if body, code := assign(bid, otherSlot, "07:30"); code != 409 {
			t.Fatalf("changed guaranteed vet status %d: %s", code, body)
		}
		assertStatus(bid, "confirmed")
		if body, code := assign(bid, slot, "07:15"); code != 200 {
			t.Fatalf("inclusive start clock %d: %s", code, body)
		}
		assertStatus(bid, "assigned")
	})

	t.Run("stale full or closed capacity cannot be assigned", func(t *testing.T) {
		slot := createSlot(vet, "vet-nazanin", day(5), "07:15", "08:45", "1")
		bid := fixture("", "vet-nazanin", "vet", "cat", "", "confirmed", "paid")
		body, code := admin.get("/management/operations")
		if code != 200 || !strings.Contains(assignmentForm(body, bid), slot) {
			t.Fatal("free slot missing before concurrent booking")
		}
		fixture(slot, "vet-nazanin", "vet", "cat", "", "paid", "paid")
		if body, code = assign(bid, slot, "07:30"); code != 409 {
			t.Fatalf("stale full slot status %d: %s", code, body)
		}
		assertStatus(bid, "confirmed")
		closed := createSlot(vet, "vet-nazanin", day(5), "21:20", "22:10", "1")
		body, code = admin.get("/management/operations")
		if code != 200 || !strings.Contains(assignmentForm(body, bid), closed) {
			t.Fatal("free slot missing before provider closed capacity")
		}
		if body, code = save(vet, day(5), "21:20", "22:10", "0"); code != 200 {
			t.Fatalf("close unbooked slot %d: %s", code, body)
		}
		if body, code = assign(bid, closed, "21:30"); code != 409 {
			t.Fatalf("stale closed slot status %d: %s", code, body)
		}
		assertStatus(bid, "confirmed")
	})

	t.Run("eligibility is rechecked instead of trusting submitted slot", func(t *testing.T) {
		slot := createSlot(vet, "vet-nazanin", day(6), "07:15", "08:45", "4")
		for _, tc := range []struct{ name, svc, species, area, provider string }{
			{"service", "grooming", "cat", "vanak", "groom-sara"},
			{"species", "vet", "rabbit", "", "vet-nazanin"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				bid := fixture("", tc.provider, tc.svc, tc.species, tc.area, "confirmed", "paid")
				if body, code := assign(bid, slot, "07:30"); code != 400 && code != 409 {
					t.Fatalf("ineligible provider slot status %d: %s", code, body)
				}
				assertStatus(bid, "confirmed")
			})
		}
		groomSlot := createSlot(groomer, "groom-sara", day(6), "07:15", "08:45", "2")
		uncovered := fixture("", "groom-sara", "grooming", "cat", "outside-coverage", "confirmed", "paid")
		if body, code := assign(uncovered, groomSlot, "07:30"); code != 400 {
			t.Fatalf("uncovered address slot status %d: %s", code, body)
		}
		assertStatus(uncovered, "confirmed")
		bid := fixture("", "vet-nazanin", "vet", "cat", "", "confirmed", "paid")
		if _, e := a.DB.Exec(ctx, "UPDATE providers SET active=false WHERE id='vet-nazanin'"); e != nil {
			t.Fatal(e)
		}
		defer a.DB.Exec(ctx, "UPDATE providers SET active=true WHERE id='vet-nazanin'")
		if body, code := assign(bid, slot, "07:30"); code != 400 && code != 409 {
			t.Fatalf("inactive provider slot status %d: %s", code, body)
		}
		assertStatus(bid, "confirmed")
	})

	t.Run("simultaneous assignments cannot exceed slot capacity", func(t *testing.T) {
		slot := createSlot(vet, "vet-nazanin", day(7), "07:15", "08:45", "1")
		ids := []string{
			fixture("", "vet-nazanin", "vet", "cat", "", "confirmed", "paid"),
			fixture("", "vet-nazanin", "vet", "cat", "", "confirmed", "paid"),
		}
		type result struct {
			code int
			err  error
		}
		results := make(chan result, len(ids))
		begin := make(chan struct{})
		var wg sync.WaitGroup
		for _, bid := range ids {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				<-begin
				values := url.Values{"csrf": {admin.csrf}, "action": {"assign"}, "availability": {slot}, "exact_clock": {"07:30"}}
				res, e := admin.client.PostForm(srv.URL+"/management/operations/bookings/"+id, values)
				if e != nil {
					results <- result{err: e}
					return
				}
				_, e = io.Copy(io.Discard, res.Body)
				_ = res.Body.Close()
				results <- result{code: res.StatusCode, err: e}
			}(bid)
		}
		close(begin)
		wg.Wait()
		close(results)
		codes := map[int]int{}
		for result := range results {
			if result.err != nil {
				t.Fatal(result.err)
			}
			codes[result.code]++
		}
		if codes[http.StatusOK] != 1 || codes[http.StatusConflict] != 1 {
			t.Fatalf("capacity race response counts: %s", fmt.Sprint(codes))
		}
		var used int
		if e := a.DB.QueryRow(ctx, "SELECT count(*) FROM bookings WHERE availability_id=$1 AND status='assigned'", slot).Scan(&used); e != nil || used != 1 {
			t.Fatalf("capacity race assigned=%d err=%v", used, e)
		}
	})

	t.Run("management capacity uses the actual end of custom windows", func(t *testing.T) {
		local := time.Now().In(a.Location)
		if local.Hour() == 0 && local.Minute() < 2 {
			t.Skip("requires at least two elapsed minutes of the current Tehran day")
		}
		request := httptest.NewRequest(http.MethodGet, "/management/reports", nil)
		baseline, e := a.loadReport(ctx, request)
		if e != nil {
			t.Fatal(e)
		}
		// Providers cannot register an ended window through the UI. A persisted
		// historical window still exists in the database and must leave the KPI.
		end := local.Add(-time.Minute).Format("15:04")
		if _, e = a.DB.Exec(ctx, `INSERT INTO availability(id,provider_id,day,period,start_time,end_time,capacity) VALUES($1,'vet-nazanin',$2,'custom','00:00',$3,11)`, token(), local.Format("2006-01-02"), end); e != nil {
			t.Fatal(e)
		}
		afterExpired, e := a.loadReport(ctx, request)
		if e != nil {
			t.Fatal(e)
		}
		if afterExpired.AvailableCapacity != baseline.AvailableCapacity {
			t.Fatalf("ended custom window changed available capacity from %d to %d", baseline.AvailableCapacity, afterExpired.AvailableCapacity)
		}
		createSlot(vet, "vet-nazanin", day(8), "21:20", "22:10", "2")
		afterFuture, e := a.loadReport(ctx, request)
		if e != nil {
			t.Fatal(e)
		}
		if afterFuture.AvailableCapacity != baseline.AvailableCapacity+2 {
			t.Fatalf("future custom window capacity=%d want %d", afterFuture.AvailableCapacity, baseline.AvailableCapacity+2)
		}
	})
}
