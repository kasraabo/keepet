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
	"time"
)

func TestExactScheduling(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	u, e := url.Parse(dsn)
	if e != nil || !strings.HasSuffix(u.Path, "_test") {
		t.Fatal("isolated test database required")
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
		if _, e := a.DB.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec("TRUNCATE users CASCADE;TRUNCATE business_config CASCADE")
	if e = a.seed(ctx); e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	owner := newBrowser(t, srv)
	owner.login("09129991110")
	vet := newBrowser(t, srv)
	vet.login("09120000002")
	groom := newBrowser(t, srv)
	groom.login("09120000003")
	var uid, session string
	a.DB.QueryRow(ctx, "SELECT id FROM users WHERE phone='09129991110'").Scan(&uid)
	a.DB.QueryRow(ctx, "SELECT token FROM sessions WHERE user_id=$1 LIMIT 1", uid).Scan(&session)
	day := time.Now().In(a.Location).AddDate(0, 0, 3).Format("2006-01-02")
	window := func(p, start, end string, capacity int) string {
		t.Helper()
		id := token()
		exec("INSERT INTO availability(id,provider_id,day,period,start_time,end_time,capacity) VALUES($1,$2,$3,'custom',$4,$5,$6)", id, p, day, start, end, capacity)
		return id
	}
	primary := window("vet-nazanin", "09:00", "11:00", 1)
	post := func(b *browser, path string, v url.Values, want int) string {
		t.Helper()
		h, c := b.post(path, v)
		if c != want {
			i := strings.Index(h, "این مرحله انجام نشد")
			if i >= 0 {
				h = h[i:]
			}
			t.Fatalf("%s got %d want %d %.800s", path, c, want, h)
		}
		return h
	}
	draft := func(svc, p string) (string, string) {
		t.Helper()
		id, key := token(), token()
		exec(`INSERT INTO bookings(id,session_token,owner_id,service,stage,pet_name,species,area,address,provider_id,sample,payment_key,request_flow,time_flow) VALUES($1,$2,$3,$4,3,'پت تست','cat','vanak','تهران نشانی تست',$5,'blood',$6,true,true)`, id, session, uid, svc, p, key)
		return id, key
	}
	choice := func(w, clock string) string {
		t.Helper()
		at, e := time.ParseInLocation("2006-01-02T15:04", day+"T"+clock, a.Location)
		if e != nil {
			t.Fatal(e)
		}
		return w + "|" + at.Format(time.RFC3339)
	}
	selectTime := func(id, w, clock string, items ...string) {
		t.Helper()
		v := url.Values{"step": {"3"}, "slot": {choice(w, clock)}}
		for _, item := range items {
			v.Add("items", item)
		}
		post(owner, "/booking/"+id+"/step", v, 200)
	}
	submit := func(id, key string) {
		t.Helper()
		post(owner, "/booking/"+id+"/request", url.Values{"payment_key": {key}}, 200)
	}
	accept := func(id string, want int) {
		t.Helper()
		post(vet, "/bookings/"+id+"/request-action", url.Values{"action": {"accept"}}, want)
	}
	t.Run("exact choice is persisted and frozen", func(t *testing.T) {
		id, key := draft("vet", "vet-nazanin")
		selectTime(id, primary, "09:00")
		submit(id, key)
		var duration int
		var at time.Time
		if e = a.DB.QueryRow(ctx, "SELECT duration_minutes,exact_time FROM bookings WHERE id=$1", id).Scan(&duration, &at); e != nil || duration != 30 || at.In(a.Location).Format("15:04") != "09:00" {
			t.Fatal(duration, at, e)
		}
		if _, e = a.DB.Exec(ctx, "UPDATE bookings SET exact_time=exact_time+interval '15 minutes' WHERE id=$1", id); e == nil {
			t.Fatal("submitted time mutable")
		}
		if _, e = a.DB.Exec(ctx, "UPDATE bookings SET duration_minutes=60 WHERE id=$1", id); e == nil {
			t.Fatal("submitted duration mutable")
		}
		accept(id, 200)
		post(owner, "/booking/"+id+"/pay", url.Values{"payment_key": {key}}, 200)
	})
	t.Run("adjacent bookings are allowed and peak is not total count", func(t *testing.T) {
		id, key := draft("vet", "vet-nazanin")
		selectTime(id, primary, "09:30")
		submit(id, key)
		accept(id, 200)
		post(owner, "/booking/"+id+"/pay", url.Values{"payment_key": {key}}, 200)
		var peak int
		a.DB.QueryRow(ctx, "SELECT booking_window_peak($1,'')", primary).Scan(&peak)
		if peak != 1 {
			t.Fatal("sequential bookings inflate capacity", peak)
		}
	})
	var holdID string
	t.Run("overlapping windows cannot bypass provider capacity", func(t *testing.T) {
		overlap := window("vet-nazanin", "09:15", "10:45", 1)
		id, key := draft("vet", "vet-nazanin")
		post(owner, "/booking/"+id+"/step", url.Values{"step": {"3"}, "slot": {choice(overlap, "09:15")}}, 409)
		selectTime(id, overlap, "10:00")
		submit(id, key)
		accept(id, 200)
		holdID = id
		wider := window("vet-nazanin", "09:00", "11:30", 4)
		other, _ := draft("vet", "vet-nazanin")
		post(owner, "/booking/"+other+"/step", url.Values{"step": {"3"}, "slot": {choice(wider, "10:15")}}, 409)
	})
	t.Run("parallel acceptance uses one shared timeline", func(t *testing.T) {
		ids := []string{}
		for i := 0; i < 2; i++ {
			id, key := draft("vet", "vet-nazanin")
			selectTime(id, primary, "10:30")
			submit(id, key)
			ids = append(ids, id)
		}
		var wg sync.WaitGroup
		codes := make(chan int, 2)
		for _, id := range ids {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				res, e := vet.client.PostForm(srv.URL+"/bookings/"+id+"/request-action", url.Values{"csrf": {vet.csrf}, "action": {"accept"}})
				if e != nil {
					codes <- 0
					return
				}
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
				codes <- res.StatusCode
			}(id)
		}
		wg.Wait()
		close(codes)
		accepted := 0
		for c := range codes {
			if c == 200 {
				accepted++
			} else if c != 409 {
				t.Fatal(c)
			}
		}
		if accepted != 1 {
			t.Fatal("double reservation", accepted)
		}
	})
	t.Run("expiration releases only the held interval", func(t *testing.T) {
		exec("UPDATE bookings SET stage_deadline=now()-interval '1 second' WHERE id=$1", holdID)
		id, key := draft("vet", "vet-nazanin")
		selectTime(id, primary, "10:00")
		submit(id, key)
		accept(id, 200)
		if e := a.reconcileRequests(ctx); e != nil {
			t.Fatal(e)
		}
		var st string
		a.DB.QueryRow(ctx, "SELECT status FROM bookings WHERE id=$1", holdID).Scan(&st)
		if st != "request_expired" {
			t.Fatal(st)
		}
	})
	t.Run("groom duration is additive and frozen with the request", func(t *testing.T) {
		items, e := a.groomTariffs(ctx, "groom-sara")
		if e != nil {
			t.Fatal(e)
		}
		v := url.Values{"revision": {"1"}}
		for _, m := range items {
			v.Set("price_"+m.ID, "100000")
			v.Set("enabled_"+m.ID, "on")
			v.Set("minutes_"+m.ID, "30")
		}
		v.Set("minutes_bath", "۴۵")
		v.Set("minutes_nails", "۱۵")
		post(groom, "/provider/pricing", v, 200)
		id, key := draft("grooming", "groom-sara")
		short := window("groom-sara", "09:00", "09:45", 1)
		post(owner, "/booking/"+id+"/step", url.Values{"step": {"3"}, "slot": {choice(short, "09:00")}, "items": {"bath", "nails"}}, 409)
		long := window("groom-sara", "09:00", "10:30", 1)
		selectTime(id, long, "09:30", "bath", "nails")
		submit(id, key)
		var duration int
		a.DB.QueryRow(ctx, "SELECT duration_minutes FROM bookings WHERE id=$1", id).Scan(&duration)
		if duration != 60 {
			t.Fatal(duration)
		}
		exec("UPDATE provider_tariffs SET duration_minutes=120 WHERE provider_id='groom-sara'")
		post(groom, "/bookings/"+id+"/request-action", url.Values{"action": {"accept"}}, 200)
		post(owner, "/booking/"+id+"/pay", url.Values{"payment_key": {key}}, 200)
		a.DB.QueryRow(ctx, "SELECT duration_minutes FROM bookings WHERE id=$1", id).Scan(&duration)
		if duration != 60 {
			t.Fatal("old duration changed", duration)
		}
	})
	t.Run("sampling uses managed visit duration", func(t *testing.T) {
		exec("UPDATE business_config SET data=jsonb_set(data,'{duration_minutes}','20') WHERE kind='service' AND id='sampling'")
		q, e := a.quote(ctx, Booking{Service: "sampling", Sample: "blood", TimeFlow: true})
		if e != nil || q.Minutes != 20 {
			t.Fatal(q, e)
		}
	})
	t.Run("nearest alternatives stay with selected provider", func(t *testing.T) {
		id, _ := draft("vet", "vet-nazanin")
		b, e := scanBooking(a.DB.QueryRow(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE id=$1", id))
		if e != nil {
			t.Fatal(e)
		}
		day80 := time.Now().In(a.Location).AddDate(0, 0, 80).Format("2006-01-02")
		r := httptest.NewRequest("GET", "/booking/"+id+"?day="+day80+"&period=night", nil)
		view := a.scheduleView(r, &b)
		if len(view.Options) != 0 || len(view.Suggestions) != 3 {
			t.Fatal("missing nearest suggestions", view.Error, len(view.Options), len(view.Suggestions))
		}
		for _, x := range view.Suggestions {
			u, e := url.Parse(x.URL)
			if e != nil || u.Query().Get("at") == "" || u.Query().Get("day") == day80 {
				t.Fatal(x)
			}
		}
	})
	t.Run("forged clocks and foreign provider choices are rejected", func(t *testing.T) {
		id, _ := draft("vet", "vet-nazanin")
		post(owner, "/booking/"+id+"/step", url.Values{"step": {"3"}, "slot": {choice(primary, "10:05")}}, 409)
		foreign := window("vet-arman", "09:00", "11:00", 1)
		post(owner, "/booking/"+id+"/step", url.Values{"step": {"3"}, "slot": {choice(foreign, "10:00")}}, 409)
	})
	t.Run("historical unknown duration is conservatively reserved", func(t *testing.T) {
		legacy := window("vet-nazanin", "15:00", "16:00", 1)
		exec("INSERT INTO bookings(id,session_token,owner_id,service,provider_id,availability_id,payment_key,amount,status,payment_status) VALUES($1,$2,$3,'vet','vet-nazanin',$4,$5,1,'accepted','paid')", token(), session, uid, legacy, token())
		id, _ := draft("vet", "vet-nazanin")
		post(owner, "/booking/"+id+"/step", url.Values{"step": {"3"}, "slot": {choice(legacy, "15:30")}}, 409)
	})
	t.Run("pre-migration pending requests cannot overbook a timed reservation", func(t *testing.T) {
		broad := window("vet-nazanin", "09:00", "11:45", 2)
		id, _ := draft("vet", "vet-nazanin")
		exec("UPDATE bookings SET time_flow=false,availability_id=$2,stage=4 WHERE id=$1", id, broad)
		stampTestQuote(t, a, id)
		exec("UPDATE bookings SET status='pending_provider',submitted_at=now(),provider_requested_at=now(),stage_deadline=now()+interval '30 minutes',approval_policy=(SELECT to_jsonb(s) FROM booking_approval_settings s WHERE id=1) WHERE id=$1", id)
		accept(id, 409)
	})

	t.Run("approval and payment deadlines never exceed appointment start", func(t *testing.T) {
		chosen := time.Now().In(a.Location).Add(2 * time.Minute).Truncate(time.Minute)
		end := chosen.Add(6 * time.Minute)
		if chosen.Day() != end.Day() {
			chosen = time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, a.Location)
			end = chosen.Add(15 * time.Minute)
		}
		exec("UPDATE business_config SET data=jsonb_set(data,'{visit_minutes}','5') WHERE kind='service' AND id='vet'")
		defer a.DB.Exec(ctx, "UPDATE business_config SET data=jsonb_set(data,'{visit_minutes}','30') WHERE kind='service' AND id='vet'")
		w := token()
		exec("INSERT INTO availability(id,provider_id,day,period,start_time,end_time,capacity) VALUES($1,'vet-nazanin',$2,'custom',$3,$4,1)", w, chosen.Format("2006-01-02"), chosen.Format("15:04"), end.Format("15:04"))
		id, key := draft("vet", "vet-nazanin")
		post(owner, "/booking/"+id+"/step", url.Values{"step": {"3"}, "slot": {w + "|" + chosen.Format(time.RFC3339)}}, 200)
		submit(id, key)
		var deadline time.Time
		a.DB.QueryRow(ctx, "SELECT stage_deadline FROM bookings WHERE id=$1", id).Scan(&deadline)
		if !deadline.Equal(chosen) {
			t.Fatal("provider deadline extends beyond appointment", deadline, chosen)
		}
		accept(id, 200)
		a.DB.QueryRow(ctx, "SELECT stage_deadline FROM bookings WHERE id=$1", id).Scan(&deadline)
		if !deadline.Equal(chosen) {
			t.Fatal("payment hold extends beyond appointment", deadline, chosen)
		}
	})

	t.Run("active capacity cannot be closed and custom tag is absent", func(t *testing.T) {
		post(vet, "/provider/availability", url.Values{"day": {day}, "period": {"custom"}, "start_time": {"09:00"}, "end_time": {"11:00"}, "capacity": {"0"}}, 409)
		if strings.Contains((Slot{Period: "custom", StartTime: "09:00", EndTime: "11:00"}).Label(), "دلخواه") {
			t.Fatal("custom tag remains")
		}
	})
}
