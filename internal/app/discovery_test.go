package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDiscovery(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated test DB required")
	}
	u, e := url.Parse(dsn)
	if e != nil || !strings.HasSuffix(u.Path, "_test") {
		t.Fatal("non-test DB")
	}
	t.Setenv("APP_MODE", "development")
	t.Setenv("DATABASE_URL", dsn)
	ctx := context.Background()
	a, e := New(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	if _, e = a.DB.Exec(ctx, "TRUNCATE users CASCADE; TRUNCATE business_config CASCADE"); e != nil {
		t.Fatal(e)
	}
	if e = a.seed(ctx); e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	owner := newBrowser(t, srv)
	owner.login("09121112345")
	admin := newBrowser(t, srv)
	admin.login("09120000005")
	if _, e = a.DB.Exec(ctx, `INSERT INTO provider_profiles(provider_id,specialties) VALUES('vet-nazanin','طب داخلی، تغذیه'),('vet-arman','جراحی'); UPDATE providers SET price=500000,experience=10 WHERE id='vet-arman'; UPDATE providers SET price=200000 WHERE id='vet-nazanin'; DELETE FROM availability WHERE provider_id='vet-arman';`); e != nil {
		t.Fatal(e)
	}
	find := func(query string, b *Booking) *DiscoveryView {
		t.Helper()
		r := httptest.NewRequest("GET", "/specialists?"+query, nil)
		v, e := a.discover(r, b)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	t.Run("price specialty query and free-capacity filters", func(t *testing.T) {
		v := find("service=vet&sort=price_desc", nil)
		if len(v.Cards) != 2 || v.Cards[0].ID != "vet-arman" {
			t.Fatal("price order", v)
		}
		v = find("service=vet&q="+url.QueryEscape("تغذیه"), nil)
		if len(v.Cards) != 1 || v.Cards[0].ID != "vet-nazanin" {
			t.Fatal("specialty text search")
		}
		v = find("service=vet&specialty="+url.QueryEscape("جراحی"), nil)
		if len(v.Cards) != 1 || v.Cards[0].ID != "vet-arman" {
			t.Fatal("specialty filter")
		}
		v = find("service=vet&available=1", nil)
		if len(v.Cards) != 1 || v.Cards[0].ID != "vet-nazanin" {
			t.Fatal("free capacity")
		}
		v = find("service=vet&min_price="+url.QueryEscape("۳۰۰٬۰۰۰"), nil)
		if len(v.Cards) != 1 || v.Cards[0].ID != "vet-arman" {
			t.Fatal("Persian min price")
		}
		if v = find("service=vet&min_price=600000&max_price=100000", nil); v.Error == "" {
			t.Fatal("invalid price range")
		}
	})
	var session, uid string
	a.DB.QueryRow(ctx, "SELECT s.token,s.user_id FROM sessions s JOIN users u ON u.id=s.user_id WHERE u.phone='09121112345' LIMIT 1").Scan(&session, &uid)
	var ratingBooking string
	t.Run("only published ratings and unrated last", func(t *testing.T) {
		id := token()
		ratingBooking = id
		_, e = a.DB.Exec(ctx, `INSERT INTO bookings(id,session_token,owner_id,service,provider_id,payment_key,status,payment_status) VALUES($1,$2,$3,'vet','vet-arman',$4,'completed','paid')`, id, session, uid, token())
		if e != nil {
			t.Fatal(e)
		}
		chat := token()
		_, e = a.DB.Exec(ctx, `INSERT INTO consultation_chats(id,booking_id,owner_id,vet_id,closed_at,duration_minutes,started_at,expires_at) VALUES($1,$2,$3,'demo-vet2',now(),30,now()-interval '1 hour',now()-interval '30 minutes')`, chat, id, uid)
		if e != nil {
			t.Fatal(e)
		}
		_, e = a.DB.Exec(ctx, `INSERT INTO consultation_customer_forms(chat_id,rating,communication,clarity,body,published) VALUES($1,5,5,5,'تست',false)`, chat)
		if e != nil {
			t.Fatal(e)
		}
		if v := find("service=vet&rating=4", nil); len(v.Cards) != 0 {
			t.Fatal("unpublished rating leaked")
		}
		_, e = a.DB.Exec(ctx, "UPDATE consultation_customer_forms SET published=true WHERE chat_id=$1", chat)
		if e != nil {
			t.Fatal(e)
		}
		for _, sortBy := range []string{"rating_asc", "rating_desc"} {
			v := find("service=vet&sort="+sortBy, nil)
			if len(v.Cards) != 2 || v.Cards[0].ID != "vet-arman" || v.Cards[0].Reviews != 1 {
				t.Fatal("unrated must be last")
			}
		}
	})
	_ = ratingBooking
	t.Run("day and custom-time windows respect capacity and interval end", func(t *testing.T) {
		day := time.Now().In(a.Location).AddDate(0, 0, 8).Format("2006-01-02")
		id := token()
		_, e = a.DB.Exec(ctx, "INSERT INTO availability(id,provider_id,day,period,start_time,end_time,capacity) VALUES($1,'vet-arman',$2,'custom','07:15','08:45',1)", id, day)
		if e != nil {
			t.Fatal(e)
		}
		if v := find("service=vet&day="+day+"&at=07:30", nil); len(v.Cards) != 1 || v.Cards[0].ID != "vet-arman" {
			t.Fatal("custom free window")
		}
		if v := find("service=vet&day="+day+"&at=08:45", nil); len(v.Cards) != 0 {
			t.Fatal("end must be exclusive")
		}
		_, e = a.DB.Exec(ctx, `INSERT INTO bookings(id,session_token,owner_id,service,provider_id,availability_id,payment_key,payment_status,status) VALUES($1,$2,$3,'vet','vet-arman',$4,$5,'paid','assigned')`, token(), session, uid, id, token())
		if e != nil {
			t.Fatal(e)
		}
		if v := find("service=vet&day="+day+"&at=07:30", nil); len(v.Cards) != 0 {
			t.Fatal("full slot advertised")
		}
	})
	t.Run("booking compatibility cannot be bypassed by query", func(t *testing.T) {
		if _, e = a.DB.Exec(ctx, "UPDATE providers SET species=ARRAY['dog'] WHERE id='vet-arman'"); e != nil {
			t.Fatal(e)
		}
		v := find("service=grooming&species=dog&sort=price_desc", &Booking{Service: "vet", Species: "cat"})
		if len(v.Cards) != 1 || v.Cards[0].ID != "vet-nazanin" {
			t.Fatal("wrong pet exposed")
		}
	})
	t.Run("admin settings permissions revision and disabled filters", func(t *testing.T) {
		if _, code := owner.get("/management/discovery"); code != 403 {
			t.Fatal("settings access")
		}
		d, e := a.discoveryConfig(ctx, "vet")
		if e != nil {
			t.Fatal(e)
		}
		c := d.Config
		c.Search = false
		for i := range c.Filters {
			if c.Filters[i].ID == "available" {
				c.Filters[i].Enabled = false
			}
		}
		_, e = a.DB.Exec(ctx, "UPDATE business_config SET data=$1 WHERE kind='discovery' AND id='vet'", mustJSON(c))
		if e != nil {
			t.Fatal(e)
		}
		if v := find("service=vet&q=no-match&available=1", nil); len(v.Cards) != 2 {
			t.Fatal("disabled controls still applied")
		}
		v := url.Values{"revision": {"999"}, "search_label": {c.SearchLabel}, "default_sort": {c.DefaultSort}, "default_layout": {"cards"}, "page_size": {"6"}}
		for i, o := range c.Filters {
			v.Set("filters_label_"+o.ID, o.Label)
			v.Set("filters_order_"+o.ID, fmtInt(int64(i)))
			if o.Enabled {
				v.Add("filters", o.ID)
			}
		}
		for i, o := range c.Sorts {
			v.Set("sorts_label_"+o.ID, o.Label)
			v.Set("sorts_order_"+o.ID, fmtInt(int64(i)))
			if o.Enabled {
				v.Add("sorts", o.ID)
			}
		}
		if _, code := admin.post("/management/discovery/settings/vet", v); code != 409 {
			t.Fatal("stale config")
		}
		v.Set("revision", fmtInt(d.Revision))
		if h, code := admin.post("/management/discovery/settings/vet", v); code != 200 {
			t.Fatalf("settings %d %.400s", code, h)
		}
		if h, code := admin.get("/management/discovery"); code != 200 || !strings.Contains(h, "تخصص و نوع پت") {
			t.Fatalf("settings render %d %.200s", code, h)
		}
	})
	t.Run("admin facts are authorized and reject stale writes", func(t *testing.T) {
		p := DiscoveryProvider{Provider: Provider{Species: []string{"cat", "dog"}}, Specialty: "طب داخلی، تغذیه"}
		v := url.Values{"original": {p.Facts()}, "species": {"cat"}, "specialty": {"طب داخلی، تغذیه"}}
		if _, c := owner.post("/management/discovery/providers/vet-nazanin", v); c != 403 {
			t.Fatal("owner facts access")
		}
		v.Set("csrf", admin.csrf)
		v.Set("original", "stale")
		if _, c := admin.post("/management/discovery/providers/vet-nazanin", v); c != 409 {
			t.Fatal("stale facts")
		}
		v.Set("original", p.Facts())
		if h, c := admin.post("/management/discovery/providers/vet-nazanin", v); c != 200 {
			t.Fatalf("facts save %d %.250s", c, h)
		}
		if x := find("service=vet&species=dog", nil); len(x.Cards) != 1 || x.Cards[0].ID != "vet-arman" {
			t.Fatal("species facts not reflected")
		}
	})
	t.Run("pet change partial in all services and cross-user protection", func(t *testing.T) {
		pet := token()
		_, e = a.DB.Exec(ctx, "INSERT INTO pets(id,user_id,name,species,age,weight,birth_year) VALUES($1,$2,'موچی','cat','۲ سال','۴','1403')", pet, uid)
		if e != nil {
			t.Fatal(e)
		}
		for _, svc := range []string{"vet", "grooming", "sampling"} {
			id := token()
			_, e = a.DB.Exec(ctx, "INSERT INTO bookings(id,session_token,owner_id,service,payment_key) VALUES($1,$2,$3,$4,$5)", id, session, uid, svc, token())
			if e != nil {
				t.Fatal(e)
			}
			body := url.Values{"pet_id": {pet}, "csrf": {owner.csrf}}
			h, code, headers := chatRequest(t, owner, "POST", "/booking/"+id+"/pet", "application/x-www-form-urlencoded", strings.NewReader(body.Encode()))
			// Non-HTMX clients follow a redirect and still receive the full form.
			if code != 200 || !strings.Contains(h, "موچی") {
				t.Fatalf("pet %s %d", svc, code)
			}
			_ = headers
			req, _ := http.NewRequest("POST", owner.base+"/booking/"+id+"/pet", strings.NewReader(body.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("HX-Request", "true")
			res, err := owner.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != 200 || res.Header.Get("HX-Retarget") != "#booking-pet-fields" || !strings.Contains(string(data), "id=\"booking-pet-fields\"") || strings.Contains(string(data), "hx-post=") {
				t.Fatalf("pet partial %s %d %s", svc, res.StatusCode, data)
			}

			if _, code := owner.post("/booking/"+id+"/pet", url.Values{"pet_id": {"someone-elses-pet"}}); code != 404 {
				t.Fatal("foreign pet accepted")
			}
		}
	})
	// Keep generated JSON valid for existing configuration tooling.
	var raw []byte
	a.DB.QueryRow(ctx, "SELECT data FROM business_config WHERE kind='discovery' AND id='vet'").Scan(&raw)
	if !json.Valid(raw) {
		t.Fatal("bad config JSON")
	}
}

func TestDiscoverySearchNormalization(t *testing.T) {
	if searchText("نازِنین كاظمي") != searchText("نازنین کاظمی") {
		t.Fatal("Persian search normalization")
	}
}
