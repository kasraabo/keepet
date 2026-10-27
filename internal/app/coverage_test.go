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

	assets "keepet"
)

func TestTehranBoundaries(t *testing.T) {
	raw, e := assets.Files.ReadFile("web/static/maps/tehran-districts.geojson")
	if e != nil {
		t.Fatal(e)
	}
	var data struct {
		Features []struct {
			Properties struct {
				ID     string
				Number int
				Label  []float64
			}
			Geometry struct {
				Type        string
				Coordinates [][][]float64
			}
		}
	}
	if e = json.Unmarshal(raw, &data); e != nil {
		t.Fatal(e)
	}
	if len(data.Features) != 22 || len(DistrictBoundaries) != 22 {
		t.Fatal("all 22 districts required")
	}
	inside := func(point []float64, ring [][]float64) bool {
		odd := false
		for i, a := range ring {
			b := ring[(i+1)%len(ring)]
			if (a[1] > point[1]) != (b[1] > point[1]) && point[0] < (b[0]-a[0])*(point[1]-a[1])/(b[1]-a[1])+a[0] {
				odd = !odd
			}
		}
		return odd
	}
	landmarks := map[int][]float64{1: {51.4727, 35.8107}, 2: {51.3753, 35.7448}, 3: {51.4099, 35.7574}, 6: {51.3955, 35.7036}, 12: {51.4205, 35.6798}, 22: {51.2758, 35.7244}}
	for i, f := range data.Features {
		n := i + 1
		if f.Properties.Number != n || f.Properties.ID != "tehran-"+strconv.Itoa(n) || f.Geometry.Type != "Polygon" || len(f.Geometry.Coordinates) == 0 {
			t.Fatal("invalid district", n)
		}
		for _, ring := range f.Geometry.Coordinates {
			if len(ring) < 4 {
				t.Fatal("degenerate border", n)
			}
			for _, p := range ring {
				if len(p) != 2 || p[0] < 51 || p[0] > 52 || p[1] < 35 || p[1] > 36 {
					t.Fatal("non-Tehran coordinates", n, p)
				}
			}
		}
		label := f.Properties.Label
		if len(label) != 2 || !inside([]float64{label[1], label[0]}, f.Geometry.Coordinates[0]) {
			t.Fatal("label outside its district", n)
		}
		if pt, ok := landmarks[n]; ok && !inside(pt, f.Geometry.Coordinates[0]) {
			t.Fatal("known Tehran landmark outside expected district", n, pt)
		}
		if DistrictBoundaries[i].ID != f.Properties.ID || !strings.HasPrefix(DistrictBoundaries[i].Path, "M") || DistrictBoundaries[i].X <= 0 || DistrictBoundaries[i].X >= 1000 || DistrictBoundaries[i].Y <= 0 || DistrictBoundaries[i].Y >= 650 {
			t.Fatal("invalid geographic SVG fallback", n)
		}
	}
}
func TestCoverageManagement(t *testing.T) {
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
	defer func() { a.DB.Exec(ctx, "TRUNCATE business_config CASCADE"); a.seedBusiness(ctx) }()
	if _, e = a.DB.Exec(ctx, "TRUNCATE users CASCADE; TRUNCATE business_config CASCADE"); e != nil {
		t.Fatal(e)
	}
	if e = a.seed(ctx); e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	admin := newBrowser(t, srv)
	admin.login("09120000005")
	ops := newBrowser(t, srv)
	ops.login("09120000001")
	owner := newBrowser(t, srv)
	owner.login("09129998888")
	for _, b := range []*browser{owner, ops} {
		if _, code := b.get("/management/coverage"); code != 403 {
			t.Fatal("coverage admin access", code)
		}
		if _, code := b.post("/management/coverage/tehran-2", url.Values{"revision": {"1"}, "enabled": {"on"}}); code != 403 {
			t.Fatal("coverage write access", code)
		}
	}
	h, c := admin.get("/management/coverage")
	if c != 200 || strings.Count(h, `data-district="tehran-`) != 22 || strings.Contains(h, "نقشه شماتیک") {
		t.Fatal("real map missing", c)
	}
	if a.enabled(ctx, "area", "tehran-2") {
		t.Fatal("new district must not silently expand coverage")
	}
	if !a.enabled(ctx, "area", "vanak") {
		t.Fatal("historic coverage lost")
	}
	save := func(on bool, want int) {
		t.Helper()
		var rev int64
		a.DB.QueryRow(ctx, "SELECT revision FROM business_config WHERE kind='area' AND id='tehran-2'").Scan(&rev)
		v := url.Values{"revision": {strconv.FormatInt(rev, 10)}}
		if on {
			v.Set("enabled", "on")
		}
		if h, c := admin.post("/management/coverage/tehran-2", v); c != want {
			t.Fatalf("coverage save %d %.150s", c, h)
		}
	}
	save(true, 200)
	if !a.enabled(ctx, "area", "tehran-2") {
		t.Fatal("enable failed")
	}
	if _, c := admin.post("/management/coverage/tehran-2", url.Values{"revision": {"1"}}); c != 409 {
		t.Fatal("stale coverage write", c)
	}
	if e = a.seedBusiness(ctx); e != nil {
		t.Fatal(e)
	}
	if !a.enabled(ctx, "area", "tehran-2") {
		t.Fatal("restart overwrote coverage")
	}
	if _, e = a.DB.Exec(ctx, "UPDATE providers SET areas=array_append(areas,'tehran-2') WHERE id='groom-sara'"); e != nil {
		t.Fatal(e)
	}
	day := time.Now().In(a.Location).AddDate(0, 0, 3).Format("2006-01-02")
	slot := token()
	if _, e = a.DB.Exec(ctx, "INSERT INTO availability(id,provider_id,day,period,capacity) VALUES($1,'groom-sara',$2,'noon',5)", slot, day); e != nil {
		t.Fatal(e)
	}
	b := Booking{Service: "grooming", ProviderID: "groom-sara", Area: "tehran-2", Species: "cat", Items: []string{"bath"}}
	slots, e := a.slots(ctx, b)
	if e != nil || len(slots) == 0 {
		t.Fatal("enabled area cannot reserve", e)
	}
	var uid, session string
	a.DB.QueryRow(ctx, "SELECT id FROM users WHERE phone='09129998888'").Scan(&uid)
	a.DB.QueryRow(ctx, "SELECT token FROM sessions WHERE user_id=$1", uid).Scan(&session)
	id, key := token(), token()
	if _, e = a.DB.Exec(ctx, `INSERT INTO bookings(id,session_token,owner_id,service,stage,pet_name,species,age,area,address,provider_id,availability_id,items,amount,payment_key) VALUES($1,$2,$3,'grooming',4,'موچی','cat','۲ سال','tehran-2','نشانی آزمایشی','groom-sara',$4,ARRAY['bath'],1,$5)`, id, session, uid, slot, key); e != nil {
		t.Fatal(e)
	}
	stampTestQuote(t, a, id)
	save(false, 200)
	slots, e = a.slots(ctx, b)
	if e != nil || len(slots) != 0 {
		t.Fatal("disabled area has bookable slots", e)
	}
	if _, c := owner.post("/booking/"+id+"/pay", url.Values{"payment_key": {key}}); c != 409 {
		t.Fatal("disabled coverage payment accepted", c)
	}
	v, e := a.discover(httptest.NewRequest("GET", "/specialists?service=grooming&area=tehran-2", nil), nil)
	if e != nil || v.Error == "" {
		t.Fatal("disabled discovery filter accepted", e)
	}
	save(true, 200)
	if _, c := owner.post("/booking/"+id+"/pay", url.Values{"payment_key": {key}}); c != 200 {
		t.Fatal("enabled coverage payment failed", c)
	}
	save(false, 200)
	var payment, area string
	a.DB.QueryRow(ctx, "SELECT payment_status,area FROM bookings WHERE id=$1", id).Scan(&payment, &area)
	if payment != "paid" || area != "tehran-2" {
		t.Fatal("coverage edit damaged paid booking")
	}
	var audit int
	a.DB.QueryRow(ctx, "SELECT count(*) FROM business_audit WHERE kind='area' AND target='tehran-2'").Scan(&audit)
	if audit != 4 {
		t.Fatal("coverage changes not audited", audit)
	}
	if _, c := admin.post("/management/coverage/fake", url.Values{"revision": {"1"}}); c != 404 {
		t.Fatal("unknown region accepted", c)
	}
}
