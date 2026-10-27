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

func TestServiceManagement(t *testing.T) {
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
	t.Setenv("SETTINGS_KEY_FILE", filepath.Join(t.TempDir(), "settings.key"))
	t.Setenv("ADMIN_SETTINGS_KEY", "")
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
	defer func() {
		if _, e := a.DB.Exec(ctx, "TRUNCATE business_config CASCADE"); e != nil {
			t.Error(e)
		}
		if e := a.seedBusiness(ctx); e != nil {
			t.Error(e)
		}
	}()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	admin, groom, owner, ops, editor, guest := newBrowser(t, srv), newBrowser(t, srv), newBrowser(t, srv), newBrowser(t, srv), newBrowser(t, srv), newBrowser(t, srv)
	admin.login("09120000005")
	groom.login("09120000003")
	owner.login("09127770001")
	ops.login("09120000001")
	editor.login("09120000007")
	post := func(b *browser, path string, v url.Values, want int) string {
		t.Helper()
		h, c := b.post(path, v)
		if c != want {
			t.Fatalf("%s %d want %d: %s", path, c, want, h)
		}
		return h
	}
	record := func(kind, id string) BusinessRecord {
		t.Helper()
		rec, e := a.editorialRecord(ctx, kind, id)
		if e != nil {
			t.Fatal(e)
		}
		return rec
	}
	for _, b := range []*browser{groom, owner, ops, editor} {
		for _, path := range []string{"/management/subservices", "/management/site-details"} {
			if _, c := b.get(path); c != 403 {
				t.Fatal("non-admin configuration", path, c)
			}
		}
	}
	for _, tc := range []struct{ key, value, message string }{{"name", "", "نام و نام خانوادگی"}, {"bio", "", "معرفی و صلاحیت حرفه‌ای"}, {"price", "bad", "تعرفهٔ پیشنهادی"}, {"license", "", "شمارهٔ نظام"}} {
		v := url.Values{"service": {"vet"}, "name": {"متقاضی آزمایشی"}, "bio": {"معرفی آزمایشی"}, "price": {"350000"}, "license": {"TEST"}}
		v.Set(tc.key, tc.value)
		if h := post(owner, "/join", v, 400); !strings.Contains(h, tc.message) {
			t.Fatal("signup error did not identify field", tc.key)
		}
	}
	const item = "test-fresh-groom"
	create := url.Values{"revision": {"0"}, "name": {"خدمت تازهٔ آزمایشی"}, "duration_minutes": {"45"}, "enabled": {"on"}, "price": {"999999"}}
	post(groom, "/management/subservices/grooming/"+item, create, 403)
	create.Del("csrf")
	post(admin, "/management/subservices/grooming/"+item, create, 200)
	r := record("grooming", item)
	if !r.Data.ProviderPriced || r.Data.Price != 0 {
		t.Fatal("new service inherited an admin price")
	}
	post(admin, "/management/grooming/"+item, url.Values{"revision": {fmt.Sprint(r.Revision)}, "name": {r.Data.Name}, "duration_minutes": {"45"}, "price": {"999999"}, "enabled": {"on"}}, 200)
	if record("grooming", item).Data.Price != 0 {
		t.Fatal("legacy admin endpoint set new provider-owned price")
	}
	if _, e := a.quote(ctx, Booking{Service: "grooming", ProviderID: "groom-sara", Items: []string{item}}); e == nil {
		t.Fatal("unpriced service orderable")
	}
	tariff := func(price string, enabled bool) {
		t.Helper()
		var rev int64
		a.DB.QueryRow(ctx, "SELECT tariff_revision FROM providers WHERE id='groom-sara'").Scan(&rev)
		values := url.Values{"revision": {fmt.Sprint(rev)}}
		menu, e := a.groomTariffs(ctx, "groom-sara")
		if e != nil {
			t.Fatal(e)
		}
		for _, m := range menu {
			values.Set("price_"+m.ID, fmt.Sprint(m.Price))
			values.Set("minutes_"+m.ID, fmt.Sprint(m.Minutes))
			if m.Enabled {
				values.Set("enabled_"+m.ID, "on")
			}
		}
		values.Set("price_"+item, price)
		values.Set("minutes_"+item, "55")
		if enabled {
			values.Set("enabled_"+item, "on")
		}
		post(groom, "/provider/pricing", values, 200)
	}
	tariff("", false) // A newly listed, inactive service must not block saving other tariffs.
	tariff("123456", true)
	exec(`UPDATE business_config SET data=jsonb_set(jsonb_set(data,'{commission}','1000'),'{fixed_fee}','2000') WHERE kind='service' AND id='grooming'`)
	quote, e := a.quote(ctx, Booking{Service: "grooming", ProviderID: "groom-sara", Items: []string{item}, TimeFlow: true})
	if e != nil || quote.Base != 123456 || quote.Total != 137802 || quote.Minutes != 55 {
		t.Fatal("provider price, fee or duration", quote, e)
	}
	const page = "svc-test-groom"
	pageValues := url.Values{"revision": {"0"}, "name": {"صفحهٔ زیرخدمت"}, "description": {"توضیح خدمت"}, "body": {"متن معرفی قابل ویرایش"}, "parent_service": {"grooming"}, "related_product": {item}, "coverage": {"vanak"}, "position": {"2"}, "enabled": {"on"}}
	post(admin, "/management/subservices/pages/"+page, pageValues, 200)
	public, c := guest.get("/services/grooming/" + page)
	if c != 200 || !strings.Contains(public, "شروع درخواست") {
		t.Fatal("service detail/booking action")
	}
	parent, _ := guest.get("/services/grooming")
	if !strings.Contains(parent, "/services/grooming/"+page) {
		t.Fatal("subservice absent from parent")
	}
	var session, uid string
	for _, cookie := range owner.client.Jar.Cookies(mustParseOnboardingURL(t, srv.URL)) {
		if cookie.Name == "keepet_session" {
			session = cookie.Value
		}
	}
	a.DB.QueryRow(ctx, "SELECT user_id FROM sessions WHERE token=$1", session).Scan(&uid)
	post(owner, "/book/grooming", url.Values{"service_page": {page}}, 200)
	var bid string
	a.DB.QueryRow(ctx, "SELECT id FROM bookings WHERE session_token=$1 ORDER BY created_at DESC LIMIT 1", session).Scan(&bid)
	b, e := scanBooking(a.DB.QueryRow(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE id=$1", bid))
	if e != nil || !contains(b.Items, item) || b.Area != "vanak" || b.Answers["_service_page"] != page {
		t.Fatal("service context not applied", e)
	}
	step := url.Values{"step": {"1"}, "pet_name": {"موچی"}, "species": {"cat"}, "age": {"۲ سال"}, "weight": {"۴"}, "area": {"tajrish"}, "address": {"نشانی کامل و طولانی آزمایشی"}}
	h := post(owner, "/booking/"+bid+"/step", step, 200)
	if !strings.Contains(h, "محلهٔ تحت پوشش") {
		t.Fatal("page coverage bypass")
	}
	step.Del("csrf")
	step.Set("area", "vanak")
	post(owner, "/booking/"+bid+"/step", step, 200)
	var retained bool
	a.DB.QueryRow(ctx, "SELECT stage=2 AND answers->>'_service_page'=$2 FROM bookings WHERE id=$1", bid, page).Scan(&retained)
	if !retained {
		t.Fatal("source context lost between steps")
	}
	h, _ = owner.get("/booking/" + bid)
	if !strings.Contains(h, money(quote.Total)) {
		t.Fatal("related service price not used in provider selection")
	}
	// The content role may edit copy, but cannot replace product or area associations.
	pageRecord := record("page", page)
	post(editor, "/content/page/"+page, url.Values{"revision": {fmt.Sprint(pageRecord.Revision)}, "name": {"عنوان تازه"}, "body": {"متن تازه"}, "enabled": {"on"}, "parent_service": {"sampling"}}, 200)
	pageRecord = record("page", page)
	if pageRecord.Data.ParentService != "grooming" || pageRecord.Data.RelatedProduct != item || !contains(pageRecord.Data.Coverage, "vanak") {
		t.Fatal("content editor changed service associations")
	}
	sitemap, _ := guest.get("/sitemap.xml")
	if !strings.Contains(sitemap, "/services/grooming/"+page) || strings.Contains(sitemap, "/pages/"+page) {
		t.Fatal("canonical service sitemap")
	}
	post(owner, "/book/sampling", url.Values{"service_page": {page}}, 409)
	exec(`UPDATE business_config SET data=jsonb_set(data,'{enabled}','false') WHERE kind='area' AND id='vanak'`)
	pageValues.Del("csrf")
	pageValues.Set("revision", fmt.Sprint(pageRecord.Revision))
	post(admin, "/management/subservices/pages/"+page, pageValues, 200)
	pageRecord = record("page", page)
	if !contains(pageRecord.Data.Coverage, "vanak") || a.enabled(ctx, "area", "vanak") {
		t.Fatal("inactive page coverage widened or enabled globally")
	}
	post(owner, "/book/grooming", url.Values{"service_page": {page}}, 409)
	exec(`UPDATE business_config SET data=jsonb_set(data,'{enabled}','true') WHERE kind='area' AND id='vanak'`)
	pageValues.Del("csrf")
	pageValues.Set("revision", fmt.Sprint(pageRecord.Revision))
	pageValues.Del("enabled")
	post(admin, "/management/subservices/pages/"+page, pageValues, 200)
	if _, c := guest.get("/services/grooming/" + page); c != 404 {
		t.Fatal("unpublished service page public")
	}
	r = record("grooming", item)
	post(admin, "/management/subservices/grooming/"+item, url.Values{"revision": {fmt.Sprint(r.Revision)}, "name": {r.Data.Name}, "duration_minutes": {"45"}}, 200)
	var stored int64
	a.DB.QueryRow(ctx, "SELECT price FROM provider_tariffs WHERE provider_id='groom-sara' AND service_id=$1", item).Scan(&stored)
	if stored != 123456 {
		t.Fatal("admin availability changed provider tariff")
	}
	if _, e := a.quote(ctx, Booking{Service: "grooming", ProviderID: "groom-sara", Items: []string{item}}); e == nil {
		t.Fatal("inactive service orderable")
	}
	// Lab cost remains an admin-owned catalog; sample pages only select an existing package.
	post(admin, "/management/subservices/pages/svc-test-sampling", url.Values{"revision": {"0"}, "name": {"صفحه آزمایش"}, "parent_service": {"sampling"}, "related_product": {"blood"}, "position": {"0"}, "enabled": {"on"}}, 200)
	post(owner, "/book/sampling", url.Values{"service_page": {"svc-test-sampling"}}, 200)
	var selected string
	a.DB.QueryRow(ctx, "SELECT sample FROM bookings WHERE session_token=$1 AND service='sampling' ORDER BY created_at DESC LIMIT 1", session).Scan(&selected)
	if selected != "blood" {
		t.Fatal("lab package not preselected")
	}
	// Public contacts and safe social links; role and revision guards.
	d := a.loadSiteDetails(ctx)
	contacts := url.Values{"revision": {fmt.Sprint(d.Revision)}, "phone": {"۰۲۱۱۲۳۴۵۶۷۸"}, "address": {"نشانی آزمایشی <script>alert(1)</script>"}, "social_label": {"شبکهٔ آزمایشی"}, "social_url": {"https://example.com/keepet"}}
	post(admin, "/management/site-details", contacts, 200)
	h, c = guest.get("/pages/about")
	if c != 200 || !strings.Contains(h, "شبکهٔ آزمایشی") || !strings.Contains(h, "tel:02112345678") || strings.Contains(h, "<script>alert(1)</script>") {
		t.Fatal("about contacts or escaping")
	}
	contacts.Del("csrf")
	post(admin, "/management/site-details", contacts, 409)
	contacts.Del("csrf")
	contacts.Set("revision", fmt.Sprint(a.loadSiteDetails(ctx).Revision))
	contacts.Set("social_url", "javascript:alert(1)")
	post(admin, "/management/site-details", contacts, 400)
	cfg := a.loadInterface(ctx)
	post(admin, "/management/interface", url.Values{"revision": {fmt.Sprint(cfg.Revision)}, "fields": {"email"}, "enabled": {"on"}}, 400)
	post(admin, "/management/interface", url.Values{"revision": {fmt.Sprint(cfg.Revision)}, "fields": {"name", "body"}}, 200)
	if a.loadInterface(ctx).Enabled {
		t.Fatal("master keyboard switch ignored")
	}
	for _, q := range []Question{{ID: "contact_email", Label: "ایمیل", Type: "text", Persian: true}, {ID: "answer", Label: "پاسخ", Type: "email", Persian: true}} {
		if validFormQuestions([]Question{q}) == nil {
			t.Fatal("English/sensitive question converted")
		}
	}
	if validFormQuestions([]Question{{ID: "notes", Label: "توضیح", Type: "textarea", Persian: true}}) != nil {
		t.Fatal("custom Persian text question rejected")
	}
	// Read-only operations filters, parameterized literal search and independent pagination.
	for i := 0; i < 29; i++ {
		exec("INSERT INTO bookings(id,session_token,owner_id,service,payment_key,provider_id,pet_name,status,payment_status,amount,created_at) VALUES($1,$2,$3,'grooming',$4,'groom-sara',$5,'accepted','paid',100000,now()-($6::int*interval '1 minute'))", fmt.Sprintf("ops-catalog-%02d", i), session, uid, token(), fmt.Sprint("پت صف ", i), i)
	}
	view := func(path string) *Page {
		t.Helper()
		p := &Page{}
		if e := a.loadOperations(httptest.NewRequest("GET", path, nil), p); e != nil {
			t.Fatal(e)
		}
		return p
	}
	list := view("/ops?sort=newest")
	if len(list.Bookings) != 25 || list.OpsView.Bookings.Count != 29 || list.OpsView.Bookings.Next == "" {
		t.Fatal("operations first page")
	}
	list = view("/ops?page=2&sort=newest")
	if len(list.Bookings) != 4 || list.OpsView.Bookings.Previous == "" {
		t.Fatal("operations next page")
	}
	list = view("/ops?booking=ops-catalog-28&page=9")
	if len(list.Bookings) != 1 || list.Bookings[0].ID != "ops-catalog-28" {
		t.Fatal("notification target lost behind pagination")
	}
	list = view("/ops?q=۰۹۱۲۷۷۷۰۰۰۱&service=grooming&status=accepted")
	if list.OpsView.Bookings.Count != 29 {
		t.Fatal("phone/service/status search")
	}
	list = view("/ops?q=" + url.QueryEscape("%' OR 1=1 --"))
	if len(list.Bookings) != 0 {
		t.Fatal("search was not parameterized/literal")
	}
	if _, c := ops.get("/ops?status=invalid"); c != 400 {
		t.Fatal("invalid operations filter accepted")
	}
	if _, c := owner.get("/ops"); c != 403 {
		t.Fatal("owner accessed queue")
	}
	h, c = guest.get("/this-page-does-not-exist")
	if c != 404 || !strings.Contains(h, "صفحهٔ موردنظر پیدا نشد") {
		t.Fatal("404 not localized")
	}
	h = post(guest, "/interface-config", nil, 405)
	if !strings.Contains(h, "روش ارسال") {
		t.Fatal("405 not localized")
	}
}
