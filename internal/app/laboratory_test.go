package app

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLaboratory(t *testing.T) {
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
	t.Setenv("ADMIN_SETTINGS_KEY", "")
	t.Setenv("SETTINGS_KEY_FILE", filepath.Join(t.TempDir(), "settings.key"))
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
	exec("TRUNCATE users CASCADE;TRUNCATE business_config;TRUNCATE laboratories,lab_tests,lab_packages,lab_package_items,lab_catalog CASCADE;INSERT INTO lab_catalog(id) VALUES(1)")
	if e = a.seed(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() {
		a.DB.Exec(ctx, "TRUNCATE laboratories,lab_tests,lab_packages,lab_package_items,lab_catalog CASCADE;INSERT INTO lab_catalog(id) VALUES(1)")
		a.seedLabs(ctx)
	}()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	actor := func(ph string) *browser { b := newBrowser(t, srv); b.login(ph); return b }
	owner := actor("09129993330")
	other := actor("09129993331")
	sampler := actor("09120000004")
	ops := actor("09120000001")
	admin := actor("09120000005")
	var uid, session, parent string
	a.DB.QueryRow(ctx, "SELECT id FROM users WHERE phone='09129993330'").Scan(&uid)
	a.DB.QueryRow(ctx, "SELECT token FROM sessions WHERE user_id=$1 LIMIT 1", uid).Scan(&session)
	a.DB.QueryRow(ctx, "SELECT id FROM users WHERE phone='09129993331'").Scan(&parent)
	post := func(b *browser, p string, v url.Values, want int) string {
		t.Helper()
		h, c := b.post(p, v)
		if c != want {
			t.Fatalf("%s got %d want %d %.1500s", p, c, want, h[max(0, strings.Index(h, "<main")):])
		}
		return h
	}
	scalar := func(q string, args ...any) int64 {
		t.Helper()
		var n int64
		if e := a.DB.QueryRow(ctx, q, args...).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	cat := func(v url.Values) { t.Helper(); post(admin, "/management/laboratories/catalog", v, 200) }
	cat(url.Values{"kind": {"lab"}, "id": {"lab-real"}, "revision": {"0"}, "name": {"آزمایشگاه تست"}, "active": {"on"}})
	for _, v := range []url.Values{{"id": {"cbc"}, "name": {"شمارش خون"}, "sample": {"blood"}, "cost": {"۱۰۰٬۰۰۰"}}, {"id": {"ur"}, "name": {"آزمایش ادرار"}, "sample": {"urine"}, "cost": {"۲۰۰٬۰۰۰"}}} {
		v.Set("kind", "test")
		v.Set("revision", "0")
		v.Set("lab_id", "lab-real")
		v.Set("active", "on")
		cat(v)
	}
	combo := url.Values{"kind": {"package"}, "id": {"combo"}, "revision": {"0"}, "name": {"پکیج ترکیبی"}, "tests": {"cbc", "ur"}, "active": {"on"}}
	cat(combo)
	post(admin, "/management/laboratories/catalog", combo, 409)
	post(ops, "/management/laboratories/catalog", combo, 403)
	post(sampler, "/management/laboratories/catalog", combo, 403)
	post(admin, "/management/laboratories/catalog", url.Values{"kind": {"test"}, "id": {"biopsy-test"}, "revision": {"0"}, "name": {"نمونه غیرمجاز"}, "sample": {"biopsy"}, "cost": {"10"}, "lab_id": {"lab-real"}}, 400)
	q, e := a.labQuote(ctx, "combo")
	if e != nil || q.Cost != 300000 || len(q.Items) != 2 {
		t.Fatal("package quote", q, e)
	}
	h, c := sampler.get("/provider/pricing")
	if c != 200 || !strings.Contains(h, "sampling_rate") {
		t.Fatal("sampler pricing", c)
	}
	post(sampler, "/provider/pricing", url.Values{"revision": {field(h, "revision")}, "sampling_rate": {"۲۵۰٬۰۰۰"}, "lab_cost": {"1"}}, 200)
	exec("UPDATE business_config SET data=jsonb_set(jsonb_set(data,'{commission}','2000'),'{fixed_fee}','10000') WHERE kind='service' AND id='sampling'")
	rc, e := a.ensureReferralCode(ctx, parent)
	if e != nil {
		t.Fatal(e)
	}
	exec("UPDATE referral_settings SET enabled=true,provider_enabled=true,sampling_bps=1000,basis='platform_share',revision=revision+1")
	exec("SELECT referral_register('demo-sampler',$1)", rc.Code)
	slot := token()
	day := time.Now().In(a.Location).AddDate(0, 0, 5).Format("2006-01-02")
	exec("INSERT INTO availability(id,provider_id,day,period,capacity) VALUES($1,'sample-amin',$2,'noon',10)", slot, day)
	id, key := token(), token()
	exec(`INSERT INTO bookings(id,session_token,owner_id,service,stage,pet_name,species,area,address,provider_id,availability_id,sample,payment_key,request_flow,fulfillment_flow,lab_flow) VALUES($1,$2,$3,'sampling',4,'موچی','cat','vanak','نشانی تست تهران','sample-amin',$4,'combo',$5,true,true,true)`, id, session, uid, slot, key)
	frozen := stampTestQuote(t, a, id)
	if frozen.Base != 250000 || frozen.Lab.Cost != 300000 || frozen.Total != 610000 || frozen.Markup != 50000 {
		t.Fatal("split economics", frozen)
	}
	post(owner, "/booking/"+id+"/request", url.Values{"payment_key": {key}}, 200)
	cat(url.Values{"kind": {"test"}, "id": {"cbc"}, "revision": {"1"}, "lab_id": {"lab-real"}, "name": {"عنوان جدید آزمایش"}, "sample": {"blood"}, "cost": {"۹۰۰٬۰۰۰"}, "active": {"on"}})
	h, _ = sampler.get("/provider/pricing")
	post(sampler, "/provider/pricing", url.Values{"revision": {field(h, "revision")}, "sampling_rate": {"۵۰۰٬۰۰۰"}}, 200)
	post(sampler, "/bookings/"+id+"/request-action", url.Values{"action": {"accept"}}, 200)
	post(owner, "/booking/"+id+"/pay", url.Values{"payment_key": {key}, "outcome": {"success"}}, 200)
	if scalar("SELECT amount FROM bookings WHERE id=$1", id) != 610000 || scalar("SELECT provider_due FROM bookings WHERE id=$1", id) != 250000 || scalar("SELECT lab_cost FROM bookings WHERE id=$1", id) != 300000 {
		t.Fatal("paid agreement changed")
	}
	if _, e := a.DB.Exec(ctx, "UPDATE bookings SET lab_cost=1 WHERE id=$1", id); e == nil {
		t.Fatal("lab cost mutable")
	}
	page := "/bookings/" + id + "/laboratory"
	if _, c := other.get(page); c != 403 {
		t.Fatal("unrelated access")
	}
	post(ops, page, url.Values{"action": {"draft"}, "body": {"زودهنگام"}}, 409)
	post(ops, "/management/laboratories/payments/"+id, url.Values{"reference": {"زودهنگام"}}, 409)
	fpage := "/bookings/" + id + "/fulfillment"
	post(sampler, fpage, url.Values{"action": {"arrived"}}, 200)
	var sealed []byte
	a.DB.QueryRow(ctx, "SELECT code_cipher FROM arrival_challenges WHERE booking_id=$1", id).Scan(&sealed)
	code, e := openIntegration("arrival:"+id, sealed)
	if e != nil {
		t.Fatal(e)
	}
	post(sampler, fpage, url.Values{"action": {"verify_arrival"}, "code": {code}}, 200)
	post(sampler, fpage, url.Values{"action": {"collected"}}, 200)
	upload := func(path, act, body string, want int) {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, v := range map[string]string{"csrf": ops.csrf, "action": act, "body": body, "lab_name": "نام دستکاری شده"} {
			mw.WriteField(k, v)
		}
		file, _ := mw.CreateFormFile("receipt", "test.png")
		file.Write(chatPNG(t))
		mw.Close()
		_, c, _ := chatRequest(t, ops, "POST", path, mw.FormDataContentType(), &buf)
		if c != want {
			t.Fatalf("upload %s %d", path, c)
		}
	}
	// Construct receipt with exactly one CSRF field, using the provider's own browser.
	var receipt bytes.Buffer
	mw := multipart.NewWriter(&receipt)
	for k, v := range map[string]string{"csrf": sampler.csrf, "action": "upload_receipt", "lab_name": "نام دستکاری شده"} {
		mw.WriteField(k, v)
	}
	part, _ := mw.CreateFormFile("receipt", "r.png")
	part.Write(chatPNG(t))
	mw.Close()
	_, c, _ = chatRequest(t, sampler, "POST", fpage, mw.FormDataContentType(), &receipt)
	if c != 200 {
		t.Fatal("handover", c)
	}
	var rid string
	a.DB.QueryRow(ctx, "SELECT handover_receipt_id FROM bookings WHERE id=$1", id).Scan(&rid)
	if scalar("SELECT count(*) FROM handover_receipts WHERE id=$1 AND lab_name='آزمایشگاه تست'", rid) != 1 {
		t.Fatal("agreed lab overwritten")
	}
	post(ops, fpage, url.Values{"action": {"approve_receipt"}, "receipt_id": {rid}}, 200)
	if scalar("SELECT cash_delta FROM wallet_entries WHERE booking_id=$1 AND kind='service_income'", id) != 250000 {
		t.Fatal("lab money paid to sampler")
	}
	if scalar("SELECT amount FROM referral_order_awards WHERE booking_id=$1 AND status='credited'", id) != 6000 {
		t.Fatal("lab money included in referral")
	}
	post(sampler, page, url.Values{"action": {"draft"}, "body": {"نمونه بردار نباید منتشر کند"}}, 403)
	upload(page, "draft", "نتیجهٔ خصوصی تست", 200)
	var report string
	a.DB.QueryRow(ctx, "SELECT id FROM lab_reports WHERE booking_id=$1 ORDER BY created_at DESC LIMIT 1", id).Scan(&report)
	for _, b := range []*browser{owner, sampler} {
		h, c := b.get(page)
		if c != 200 || strings.Contains(h, "نتیجهٔ خصوصی تست") {
			t.Fatal("draft leaked", c)
		}
		_, c, _ = chatRequest(t, b, "GET", page+"/"+report, "", nil)
		if c != 404 {
			t.Fatal("private draft file leaked", c)
		}
		post(b, page, url.Values{"action": {"publish"}, "report_id": {report}}, 403)
	}
	post(ops, page, url.Values{"action": {"publish"}, "report_id": {report}}, 200)
	post(ops, page, url.Values{"action": {"publish"}, "report_id": {report}}, 200)
	for _, b := range []*browser{owner, sampler, ops} {
		h, c := b.get(page)
		if c != 200 || !strings.Contains(h, "نتیجهٔ خصوصی تست") {
			t.Fatal("published report unavailable", c)
		}
		_, c, head := chatRequest(t, b, "GET", page+"/"+report, "", nil)
		if c != 200 || head.Get("Cache-Control") != "private, no-store" {
			t.Fatal("published attachment", c)
		}
	}
	_, c, _ = chatRequest(t, other, "GET", page+"/"+report, "", nil)
	if c != 404 {
		t.Fatal("unrelated result leaked")
	}
	if _, e := a.DB.Exec(ctx, "UPDATE lab_reports SET body='edited' WHERE id=$1", report); e == nil {
		t.Fatal("published report mutable")
	}
	post(ops, page, url.Values{"action": {"draft"}, "body": {"پیش‌نویس قدیمی‌تر"}}, 200)
	var staleReport string
	a.DB.QueryRow(ctx, "SELECT id FROM lab_reports WHERE booking_id=$1 AND state='draft' ORDER BY created_at DESC LIMIT 1", id).Scan(&staleReport)
	post(ops, page, url.Values{"action": {"draft"}, "body": {"نتیجهٔ اصلاحی"}}, 200)
	var report2 string
	a.DB.QueryRow(ctx, "SELECT id FROM lab_reports WHERE booking_id=$1 ORDER BY created_at DESC LIMIT 1", id).Scan(&report2)
	post(ops, page, url.Values{"action": {"publish"}, "report_id": {report2}}, 200)
	post(ops, page, url.Values{"action": {"publish"}, "report_id": {staleReport}}, 409)
	if scalar("SELECT count(*) FROM lab_reports WHERE booking_id=$1 AND state='published'", id) != 2 || scalar("SELECT count(*) FROM wallet_entries WHERE booking_id=$1 AND kind='service_income'", id) != 1 || scalar("SELECT count(*) FROM referral_events WHERE booking_id=$1 AND kind='order_income'", id) != 1 {
		t.Fatal("result versioning or duplicate money")
	}
	for _, b := range []*browser{ops, admin} {
		post(b, "/management/laboratories/payments/"+id, url.Values{"reference": {"پرداخت آزمایشگاه تست"}, "amount": {"1"}}, 200)
	}
	if scalar("SELECT amount FROM lab_payments WHERE booking_id=$1", id) != 300000 {
		t.Fatal("wrong lab payment")
	}
	post(owner, "/management/laboratories/payments/"+id, url.Values{"reference": {"غیر مجاز"}}, 403)
	if h, c := admin.get("/management/finance"); c != 200 || !strings.Contains(h, "۶۰٬۰۰۰") {
		t.Fatal("financial fee includes lab", c)
	}
	if h, c := admin.get("/management/export.csv"); c != 200 || !strings.Contains(h, "laboratory_cost_toman") {
		t.Fatal("laboratory cost missing from finance export", c)
	}
	cat(url.Values{"kind": {"lab"}, "id": {"lab-real"}, "revision": {"1"}, "name": {"آزمایشگاه تست"}})
	if a.labPackageAvailable(ctx, "combo") {
		t.Fatal("disabled lab still offered")
	}
	if h, c := owner.get(page); c != 200 || !strings.Contains(h, "آزمایشگاه تست") {
		t.Fatal("history lost after disabling")
	}
	post(owner, "/book/sampling", nil, 200)
	if scalar("SELECT count(*) FROM bookings WHERE owner_id=$1 AND status='draft' AND lab_flow", uid) != 1 {
		t.Fatal("new order did not opt into laboratory flow")
	}
}
