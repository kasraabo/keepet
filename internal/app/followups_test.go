package app

import (
	"context"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCareFollowups(t *testing.T) {
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
	exec("TRUNCATE users CASCADE;TRUNCATE business_config CASCADE")
	if e = a.seed(ctx); e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	actor := func(ph string) *browser { b := newBrowser(t, srv); b.login(ph); return b }
	owner := actor("09129995550")
	other := actor("09129995551")
	vet := actor("09120000002")
	vet2 := actor("09120000006")
	groom := actor("09120000003")
	admin := actor("09120000005")
	var uid, session string
	a.DB.QueryRow(ctx, "SELECT id FROM users WHERE phone='09129995550'").Scan(&uid)
	a.DB.QueryRow(ctx, "SELECT token FROM sessions WHERE user_id=$1 LIMIT 1", uid).Scan(&session)
	scalar := func(q string, args ...any) int64 {
		t.Helper()
		var n int64
		if e := a.DB.QueryRow(ctx, q, args...).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	post := func(b *browser, p string, v url.Values, want int) string {
		t.Helper()
		copy := url.Values{}
		for k, vs := range v {
			copy[k] = append([]string{}, vs...)
		}
		copy.Set("csrf", b.csrf)
		h, c := b.post(p, copy)
		if c != want {
			t.Fatalf("%s got %d want %d %.1600s", p, c, want, h[max(0, strings.Index(h, "<main")):])
		}
		return h
	}
	pid, otherPet, source := token(), token(), token()
	for _, id := range []string{pid, otherPet} {
		exec("INSERT INTO pets(id,user_id,name,species,age,weight,birth_year) VALUES($1,$2,'موچی','cat','۲ سال','۴','1403')", id, uid)
	}
	exec(`INSERT INTO bookings(id,session_token,owner_id,service,stage,pet_name,species,age,weight,answers,provider_id,payment_key,payment_status,status,amount,provider_due,paid_at,fulfillment_flow) VALUES($1,$2,$3,'vet',4,'موچی','cat','۲ سال','۴',jsonb_build_object('_pet_id',$4::text,'birth_year','1403'),'vet-nazanin',$5,'paid','accepted',350000,280000,now(),true)`, source, session, uid, pid, token())
	post(vet, "/provider/bookings/"+source, url.Values{"action": {"start"}}, 200)
	var cid string
	a.DB.QueryRow(ctx, "SELECT id FROM consultation_chats WHERE booking_id=$1", source).Scan(&cid)
	form := "/chats/" + cid + "/follow-up"
	if _, c := vet.get(form); c != 409 {
		t.Fatal("offered before completed report", c)
	}
	post(vet, "/chats/"+cid+"/questionnaire", url.Values{"symptoms": {"شرح علائم"}, "diagnosis": {"تشخیص"}, "prescription": {"نسخه قابل مشاهده"}, "customer_feedback": {"یادداشت خصوصی پزشک"}}, 200)
	post(vet, "/chats/"+cid+"/end", nil, 200)
	h, c := vet.get("/chats/" + cid)
	if c != 200 || !strings.Contains(h, "پیشنهاد جلسه یا خدمت بعدی") {
		t.Fatal("missing followup entry", c)
	}
	h, c = vet.get(form)
	if c != 200 {
		t.Fatal("proposal form", c)
	}
	key := field(h, "form_key")
	if len(key) != 64 {
		t.Fatal("form key")
	}
	day := time.Now().In(a.Location).AddDate(0, 0, 3).Format("2006-01-02")
	slots, e := a.exactSlots(ctx, Booking{Service: "vet", ProviderID: "vet-nazanin", Species: "cat", TimeFlow: true, Status: "draft"})
	if e != nil {
		t.Fatal(e)
	}
	var preferred Slot
	for _, s := range slots {
		if s.Day == day && s.Period == "morning" {
			preferred = s
			break
		}
	}
	if preferred.ID == "" {
		t.Fatal("no fixture slot")
	}
	values := url.Values{"form_key": {key}, "service": {"vet"}, "day": {day}, "period": {"morning"}, "slot": {preferred.Choice()}, "note": {"بازبینی سلامتی"}}
	baseline := scalar("SELECT count(*) FROM wallet_entries")
	for _, b := range []*browser{vet2, groom} {
		post(b, form, values, 403)
	}
	post(owner, form, values, 403)
	post(admin, form, values, 403)
	post(vet, form, values, 200)
	post(vet, form, values, 200)
	var fid string
	a.DB.QueryRow(ctx, "SELECT id FROM care_followups WHERE source_booking_id=$1 AND service='vet'", source).Scan(&fid)
	if scalar("SELECT count(*) FROM care_followups WHERE source_booking_id=$1", source) != 1 || scalar("SELECT count(*) FROM bookings") != 1 || scalar("SELECT count(*) FROM wallet_entries") != baseline {
		t.Fatal("proposal created booking/money or duplicate")
	}
	detail := "/follow-ups/" + fid
	if _, c := other.get(detail); c != 404 {
		t.Fatal("foreign followup exposed", c)
	}
	if h, c := owner.get(detail); c != 200 || !strings.Contains(h, "بازبینی سلامتی") || strings.Contains(h, "یادداشت خصوصی پزشک") {
		t.Fatal("care content/privacy", c)
	}
	if scalar("SELECT count(*) FROM notifications WHERE followup_id=$1 AND user_id=$2", fid, uid) != 1 {
		t.Fatal("patient notification")
	}
	var nid string
	a.DB.QueryRow(ctx, "SELECT id::text FROM notifications WHERE followup_id=$1 AND user_id=$2", fid, uid).Scan(&nid)
	post(owner, "/notifications/"+nid+"/read", nil, 200)
	post(vet, detail+"/action", url.Values{"action": {"start"}}, 403)
	post(admin, detail+"/action", url.Values{"action": {"start"}}, 404)
	if _, e := a.DB.Exec(ctx, "UPDATE care_followups SET note='changed' WHERE id=$1", fid); e == nil {
		t.Fatal("recommendation mutable")
	}
	values.Set("form_key", token())
	post(vet, form, values, 409)
	// Repeated acceptance from the same patient must reuse one draft.
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, c := owner.post(detail+"/action", url.Values{"action": {"start"}})
			codes <- c
		}()
	}
	wg.Wait()
	close(codes)
	for c := range codes {
		if c != 200 {
			t.Fatal("concurrent start", c)
		}
	}
	var bid string
	a.DB.QueryRow(ctx, "SELECT booking_id FROM care_followups WHERE id=$1", fid).Scan(&bid)
	if scalar("SELECT count(*) FROM bookings WHERE followup_id=$1", fid) != 1 {
		t.Fatal("duplicate followup drafts")
	}
	if scalar("SELECT count(*) FROM bookings WHERE id=$1 AND payment_status<>'paid' AND status='draft' AND request_flow AND time_flow AND fulfillment_flow AND answers->>'_pet_id'=$2", bid, pid) != 1 {
		t.Fatal("draft did not preserve patient/workflow")
	}
	post(owner, detail+"/action", url.Values{"action": {"dismiss"}}, 409)
	post(vet, detail+"/action", url.Values{"action": {"withdraw"}}, 409)
	post(owner, "/booking/"+bid+"/pet", url.Values{"pet_id": {otherPet}}, 409)
	if _, e := a.DB.Exec(ctx, "UPDATE bookings SET answers=jsonb_set(answers,'{_pet_id}',to_jsonb($2::text)) WHERE id=$1", bid, otherPet); e == nil {
		t.Fatal("care pet changed")
	}
	// A newly required pet question must still be answered; existing core values remain reusable.
	originalQuestions := a.form(ctx, "pet", nil).Questions
	qs := append([]Question{}, originalQuestions...)
	qs = append(qs, textQ("current_health", "وضعیت تازهٔ پت", true))
	exec("UPDATE business_config SET data=jsonb_set(data,'{questions}',$1::jsonb) WHERE kind='form' AND id='pet'", string(mustJSON(qs)))
	defer a.DB.Exec(ctx, "UPDATE business_config SET data=jsonb_set(data,'{questions}',$1::jsonb) WHERE kind='form' AND id='pet'", string(mustJSON(originalQuestions)))
	input := url.Values{"step": {"1"}, "selected_pet": {pid}, "pet_name": {"موچی"}, "species": {"cat"}, "symptom": {"skin"}, "duration": {"today"}, "consent": {"on"}}
	post(owner, "/booking/"+bid+"/step", input, 200)
	if scalar("SELECT stage FROM bookings WHERE id=$1", bid) != 1 {
		t.Fatal("new required pet field was bypassed")
	}
	input.Set("current_health", "وضعیت فعلی بررسی شد")
	post(owner, "/booking/"+bid+"/step", input, 200)
	h, c = owner.get("/booking/" + bid)
	if c != 200 || !strings.Contains(h, `value="vet-nazanin" checked`) {
		t.Fatal("same treating vet was not suggested", c)
	}
	exec("UPDATE providers SET hourly_rate=900000,tariff_revision=tariff_revision+1 WHERE id='vet-nazanin'")
	exec("UPDATE availability SET capacity=0 WHERE id=$1", preferred.ID)
	h = post(owner, "/booking/"+bid+"/step", url.Values{"step": {"2"}, "provider": {"vet-nazanin"}}, 200)
	if !strings.Contains(h, "نزدیک‌ترین زمان‌های آزاد") {
		t.Fatal("no nearby alternative after preferred slot filled")
	}
	b, e := scanBooking(a.DB.QueryRow(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE id=$1", bid))
	if e != nil {
		t.Fatal(e)
	}
	available, e := a.exactSlots(ctx, b)
	if e != nil || len(available) == 0 {
		t.Fatal("nearest slots", e)
	}
	post(owner, "/booking/"+bid+"/step", url.Values{"step": {"3"}, "slot": {available[0].Choice()}}, 200)
	if scalar("SELECT amount FROM bookings WHERE id=$1", bid) != 450000 {
		t.Fatal("followup reused prior price")
	}
	var paymentKey string
	a.DB.QueryRow(ctx, "SELECT payment_key FROM bookings WHERE id=$1", bid).Scan(&paymentKey)
	post(owner, "/booking/"+bid+"/request", url.Values{"payment_key": {paymentKey}}, 200)
	if scalar("SELECT count(*) FROM bookings WHERE id=$1 AND status='pending_provider' AND payment_status<>'paid'", bid) != 1 {
		t.Fatal("followup bypassed approval")
	}
	post(vet, "/bookings/"+bid+"/request-action", url.Values{"action": {"reject"}}, 200)
	post(owner, "/bookings/"+bid+"/request-action", url.Values{"action": {"retry"}}, 200)
	var retry string
	a.DB.QueryRow(ctx, "SELECT booking_id FROM care_followups WHERE id=$1", fid).Scan(&retry)
	if retry == bid || scalar("SELECT count(*) FROM bookings WHERE id=$1 AND followup_id=$2", retry, fid) != 1 {
		t.Fatal("retry lineage lost")
	}
	post(owner, "/bookings/"+bid+"/request-action", url.Values{"action": {"retry"}}, 200)
	if scalar("SELECT count(*) FROM bookings WHERE followup_id=$1", fid) != 2 {
		t.Fatal("repeated retry forked a new draft")
	}
	if scalar("SELECT count(*) FROM wallet_entries") != baseline {
		t.Fatal("care actions created a financial operation")
	}
	// A second service may be recommended, but is still a fresh patient-approved flow.
	values.Set("form_key", token())
	values.Set("service", "sampling")
	values.Del("slot")
	post(vet, form, values, 200)
	var labFID string
	a.DB.QueryRow(ctx, "SELECT id FROM care_followups WHERE source_booking_id=$1 AND service='sampling'", source).Scan(&labFID)
	post(owner, "/follow-ups/"+labFID+"/action", url.Values{"action": {"start"}}, 200)
	if scalar("SELECT count(*) FROM bookings WHERE followup_id=$1 AND service='sampling' AND lab_flow AND status='draft'", labFID) != 1 {
		t.Fatal("service-specific laboratory flow missing")
	}
	values.Set("form_key", token())
	values.Set("service", "grooming")
	post(vet, form, values, 200)
	var groomFID string
	a.DB.QueryRow(ctx, "SELECT id FROM care_followups WHERE source_booking_id=$1 AND service='grooming'", source).Scan(&groomFID)
	post(owner, "/follow-ups/"+groomFID+"/action", url.Values{"action": {"dismiss"}}, 200)
	post(owner, "/follow-ups/"+groomFID+"/action", url.Values{"action": {"start"}}, 409)
	values.Set("form_key", token())
	post(vet, form, values, 200)
	a.DB.QueryRow(ctx, "SELECT id FROM care_followups WHERE source_booking_id=$1 AND service='grooming' AND state='offered'", source).Scan(&groomFID)
	post(vet, "/follow-ups/"+groomFID+"/action", url.Values{"action": {"withdraw"}}, 200)
	post(owner, "/follow-ups/"+groomFID+"/action", url.Values{"action": {"start"}}, 409)
	exec("UPDATE providers SET active=false WHERE id='vet-nazanin'")
	h, c = owner.get("/booking/" + retry)
	if c != 200 || strings.Contains(h, `value="vet-nazanin" checked`) || !strings.Contains(h, `value="vet-arman"`) {
		t.Fatal("unavailable treating vet blocked alternatives", c)
	}
	exec("UPDATE providers SET active=true WHERE id='vet-nazanin'")

	for _, path := range []string{"/follow-ups", "/dashboard", "/chats/" + cid} {
		if h, c := owner.get(path); c != 200 || !strings.Contains(h, "ادامهٔ مراقبت") {
			t.Fatal("care entry missing", path, c)
		}
	}
	if _, c := owner.get("/management/follow-ups"); c != 403 {
		t.Fatal("management scope")
	}
	if _, c := admin.get("/management/follow-ups"); c != 200 {
		t.Fatal("management list", c)
	}
}
