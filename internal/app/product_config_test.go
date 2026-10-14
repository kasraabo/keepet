package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPersianCalendar(t *testing.T) {
	for _, pair := range [][2]string{{"1403/01/01", "2024-03-20"}, {"1403/12/30", "2025-03-20"}, {"1404/01/01", "2025-03-21"}, {"1405/07/22", "2026-10-14"}} {
		d, e := parseJalali(pair[0])
		if e != nil || d.Format("2006-01-02") != pair[1] {
			t.Fatalf("%v: %v %v", pair, d, e)
		}
		if normalize(jalaliDate(pair[1])) != pair[0] {
			t.Fatal("roundtrip", pair)
		}
	}
	for _, s := range []string{"1404/12/30", "1405/07/31", "1405/13/01", "۱۴۰۵/۰۱/۰۰"} {
		if _, e := parseJalali(s); e == nil {
			t.Fatal("invalid date accepted", s)
		}
	}
	for day := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC); day.Before(time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC)); day = day.AddDate(0, 0, 1) {
		s := jalaliDate(day.Format("2006-01-02"))
		back, e := parseJalali(s)
		if e != nil || !back.Equal(day) {
			t.Fatal("calendar roundtrip", s, day, e)
		}
	}
}
func TestProductConfiguration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated Postgres required")
	}
	t.Setenv("APP_MODE", "development")
	t.Setenv("DATABASE_URL", dsn)
	a, e := New(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	ctx := context.Background()
	if _, e = a.DB.Exec(ctx, "TRUNCATE users CASCADE"); e != nil {
		t.Fatal(e)
	}
	if e = a.seed(ctx); e != nil {
		t.Fatal(e)
	}
	snapshot, _ := a.records(ctx, "")
	defer func() {
		a.DB.Exec(ctx, "DELETE FROM business_config")
		for _, v := range snapshot {
			a.DB.Exec(ctx, "INSERT INTO business_config(kind,id,data,revision) VALUES($1,$2,$3,$4)", v.Kind, v.ID, mustJSON(v.Data), v.Revision)
		}
	}()
	server := httptest.NewServer(a.Handler())
	defer server.Close()
	admin := newBrowser(t, server)
	admin.login("09120000005")
	owner := newBrowser(t, server)
	owner.login("09121112220")
	stranger := newBrowser(t, server)
	stranger.login("09121112221")
	content := newBrowser(t, server)
	content.login("09120000007")
	vet := newBrowser(t, server)
	vet.login("09120000002")
	t.Run("question editor permissions revisions and validation", func(t *testing.T) {
		f := a.form(ctx, "grooming", nil)
		f.Questions = append(f.Questions, Question{ID: "coat", Label: "نوع مو", Type: "select", Required: true, Options: []QuestionOption{{"short", "کوتاه"}, {"long", "بلند"}}})
		vs := url.Values{"questions": {string(mustJSON(f.Questions))}, "revision": {fmt.Sprint(f.Revision)}}
		if _, code := owner.post("/management/questionnaires/grooming", vs); code != 403 {
			t.Fatal("owner changed schema", code)
		}
		vs.Del("csrf")
		if body, code := admin.post("/management/questionnaires/grooming", vs); code != 200 || !strings.Contains(body, "نوع مو") {
			t.Fatal("save schema", code, body)
		}
		if _, code := admin.post("/management/questionnaires/grooming", vs); code != 409 {
			t.Fatal("stale schema accepted")
		}
		r := httptest.NewRequest("POST", "/", strings.NewReader("coat=bad"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.ParseForm()
		if _, e := formAnswers(r, a.form(ctx, "grooming", nil)); e == nil {
			t.Fatal("forged option accepted")
		}
		f = a.form(ctx, "pet", nil)
		f.Questions = f.Questions[1:]
		if _, code := admin.post("/management/questionnaires/pet", url.Values{"revision": {fmt.Sprint(f.Revision)}, "questions": {string(mustJSON(f.Questions))}}); code != 400 {
			t.Fatal("core field removed")
		}
	})
	t.Run("pet age fallback selection and profile reuse", func(t *testing.T) {
		if _, code := owner.post("/pets", url.Values{"pet_name": {"موچی"}, "species": {"cat"}}); code != 400 {
			t.Fatal("missing age accepted")
		}
		if _, code := owner.post("/pets", url.Values{"pet_name": {"موچی"}, "species": {"cat"}, "birth_year": {"۱۴۰۳"}, "weight": {"۴"}}); code != 200 {
			t.Fatal("birth year rejected", code)
		}
		var id, uid string
		a.DB.QueryRow(ctx, "SELECT p.id,p.user_id FROM pets p JOIN users u ON u.id=p.user_id WHERE u.phone='09121112220'").Scan(&id, &uid)
		owner.post("/book/vet", nil)
		var bookingID string
		a.DB.QueryRow(ctx, "SELECT id FROM bookings WHERE owner_id=$1 AND status='draft' ORDER BY created_at DESC LIMIT 1", uid).Scan(&bookingID)
		if _, code := stranger.post("/booking/"+bookingID+"/pet", url.Values{"pet_id": {id}}); code != 404 {
			t.Fatal("foreign booking accessible")
		}
		body, code := owner.post("/booking/"+bookingID+"/pet", url.Values{"pet_id": {id}})
		if code != 200 || !strings.Contains(body, "اطلاعات از پروفایل پت") {
			t.Fatal("pet not loaded", code)
		}
		body, code = owner.post("/booking/"+bookingID+"/step", url.Values{"step": {"1"}, "pet_name": {"spoofed"}, "species": {"dog"}, "symptom": {"skin"}, "duration": {"today"}, "skin_details": {"شرح"}, "consent": {"on"}})
		if code != 200 || !strings.Contains(body, "همراه مناسب") {
			t.Fatal("reuse failed", code, body)
		}
		var name, sp, age string
		a.DB.QueryRow(ctx, "SELECT pet_name,species,age FROM bookings WHERE id=$1", bookingID).Scan(&name, &sp, &age)
		if name != "موچی" || sp != "cat" || age == "" {
			t.Fatal("pet identity forged", name, sp, age)
		}
		legacy := token()
		a.DB.Exec(ctx, "INSERT INTO pets(id,user_id,name,species) VALUES($1,$2,'پت قدیمی','cat')", legacy, uid)
		owner.post("/book/vet", nil)
		a.DB.QueryRow(ctx, "SELECT id FROM bookings WHERE owner_id=$1 AND status='draft' ORDER BY created_at DESC LIMIT 1", uid).Scan(&bookingID)
		body, code = owner.post("/booking/"+bookingID+"/pet", url.Values{"pet_id": {legacy}})
		if code != 200 || !strings.Contains(body, "سن تقریبی اگر") {
			t.Fatal("legacy age field missing")
		}
		body, code = owner.post("/booking/"+bookingID+"/step", url.Values{"step": {"1"}, "pet_name": {"پت قدیمی"}, "species": {"cat"}, "birth_year": {"1400"}, "symptom": {"skin"}, "duration": {"today"}, "skin_details": {"شرح"}, "consent": {"on"}})
		if code != 200 || !strings.Contains(body, "همراه مناسب") {
			t.Fatal("legacy pet birth fallback failed", code, body)
		}

	})
	t.Run("FAQ email and editorial separation", func(t *testing.T) {
		if _, code := owner.post("/support", url.Values{"subject": {"پیگیری رزرو"}, "body": {"شرح درخواست"}, "email": {"invalid"}}); code != 400 {
			t.Fatal("invalid email accepted")
		}
		if _, code := owner.post("/support", url.Values{"subject": {"پیگیری رزرو"}, "body": {"شرح درخواست"}, "email": {"qa@example.com"}}); code != 200 {
			t.Fatal("ticket rejected")
		}
		if _, code := content.get("/management"); code != 403 {
			t.Fatal("content editor entered business")
		}
		if body, code := content.get("/content"); code != 200 || strings.Contains(body, "class=\"header\"") {
			t.Fatal("independent editor missing", code)
		}
		if _, code := content.post("/content/faq/editor-faq", url.Values{"name": {"FAQ"}, "body": {"text"}, "enabled": {"on"}}); code != 403 {
			t.Fatal("editor modified support")
		}
		if _, code := admin.post("/content/faq/test-faq", url.Values{"name": {"سؤال آزمایشی"}, "body": {"پاسخ قابل تغییر"}, "enabled": {"on"}}); code != 200 {
			t.Fatal("faq creation failed")
		}
		body, _ := owner.get("/support")
		if !strings.Contains(body, "پاسخ قابل تغییر") {
			t.Fatal("faq not live")
		}
	})
	t.Run("SEO metadata safe text and publication", func(t *testing.T) {
		vs := url.Values{"name": {"عنوان سئو تست"}, "link": {"/services/grooming"}, "description": {"توضیح آزمایشی"}, "body": {"<script>alert(1)</script> متن سئو"}, "enabled": {"on"}, "revision": {"1"}}
		var rev int
		a.DB.QueryRow(ctx, "SELECT revision FROM business_config WHERE kind='seo' AND id='grooming'").Scan(&rev)
		vs.Set("revision", fmt.Sprint(rev))
		if body, code := content.post("/content/seo/grooming", vs); code != 200 {
			t.Fatal("seo save", code, body)
		}
		guest := newBrowser(t, server)
		body, code := guest.get("/services/grooming")
		if code != 200 || !strings.Contains(body, "عنوان سئو تست") || !strings.Contains(body, "&lt;script&gt;") || strings.Contains(body, "<script>alert") {
			t.Fatal("metadata escaping failed")
		}
		if !strings.Contains(body, "rel=\"canonical\"") {
			t.Fatal("canonical absent")
		}
		var pageRev int
		a.DB.QueryRow(ctx, "SELECT revision FROM business_config WHERE kind='page' AND id='about'").Scan(&pageRev)
		if _, code := content.post("/content/page/about", url.Values{"revision": {fmt.Sprint(pageRev)}, "name": {"دربارهٔ کیپت"}, "body": {"متن تازه دربارهٔ ما"}, "enabled": {"on"}}); code != 200 {
			t.Fatal("existing page not editable")
		}
		edited, _ := guest.get("/pages/about")
		if !strings.Contains(edited, "متن تازه دربارهٔ ما") {
			t.Fatal("existing page text not live")
		}
		magazine, _ := guest.get("/pages/magazine")
		if !strings.Contains(magazine, "مقاله‌های بازبینی‌شده") {
			t.Fatal("magazine overridden by seed")
		}
		if _, code := owner.get("/content"); code != 403 {
			t.Fatal("owner entered editor")
		}
		body, code = guest.get("/sitemap.xml")
		if code != 200 || !strings.Contains(body, "/specialists/vet-nazanin") || strings.Contains(body, "/chats/") {
			t.Fatal("sitemap wrong")
		}
	})
	t.Run("profile ownership and moderated genuine rating", func(t *testing.T) {
		if _, code := owner.post("/provider/profile", url.Values{"name": {"hack"}}); code != 403 {
			t.Fatal("profile forged")
		}
		if _, code := vet.post("/provider/profile", url.Values{"name": {"دکتر نازنین مهر"}, "bio": {"معرفی تازه"}, "experience": {"8"}, "specialties": {"طب داخلی"}, "education": {"دامپزشکی"}, "background": {"کلینیک نمونه"}}); code != 200 {
			t.Fatal("profile update failed")
		}
		guest := newBrowser(t, server)
		home, _ := guest.get("/")
		if strings.Contains(home, `href="/specialists/vet"`) {
			t.Fatal("service linked to specialist instead of landing")
		}
		sampling, _ := guest.get("/services/sampling")
		if strings.Contains(sampling, "/specialists/sample-amin") {
			t.Fatal("sampling linked to missing profile")
		}
		body, _ := guest.get("/specialists/vet-nazanin")
		if !strings.Contains(body, "طب داخلی") || !strings.Contains(body, "هنوز نظر تأییدشده‌ای") {
			t.Fatal("profile data missing")
		}
		if _, code := guest.get("/specialists/sample-amin"); code != 404 {
			t.Fatal("sampling profile unexpectedly public")
		}
	})
	t.Run("dynamic clinical questions preserve confidentiality", func(t *testing.T) {
		f := a.form(ctx, "vet-report", nil)
		f.Questions = append(f.Questions, Question{ID: "follow_up", Label: "پیگیری بالینی", Type: "text"}, Question{ID: "private_note", Label: "یادداشت داخلی", Type: "text", Private: true})
		if _, code := admin.post("/management/questionnaires/vet-report", url.Values{"revision": {fmt.Sprint(f.Revision)}, "questions": {string(mustJSON(f.Questions))}}); code != 200 {
			t.Fatal("clinical editor rejected")
		}
		var uid, session string
		a.DB.QueryRow(ctx, "SELECT u.id,s.token FROM users u JOIN sessions s ON s.user_id=u.id WHERE u.phone='09121112220' AND s.expires_at>now() LIMIT 1").Scan(&uid, &session)
		bid, cid := token(), token()
		_, e := a.DB.Exec(ctx, "INSERT INTO bookings(id,session_token,owner_id,service,provider_id,pet_name,status,payment_status,payment_key) VALUES($1,$2,$3,'vet','vet-nazanin','موچی','in_progress','paid',$4)", bid, session, uid, token())
		if e != nil {
			t.Fatal(e)
		}
		_, e = a.DB.Exec(ctx, "INSERT INTO consultation_chats(id,booking_id,owner_id,vet_id,started_at,expires_at,duration_minutes) VALUES($1,$2,$3,'demo-vet',now(),now()+interval '30 minutes',30)", cid, bid, uid)
		if e != nil {
			t.Fatal(e)
		}
		if body, code := vet.post("/chats/"+cid+"/questionnaire", url.Values{"symptoms": {"علائم"}, "diagnosis": {"تشخیص"}, "prescription": {"توصیه"}, "follow_up": {"پیگیری قابل مشاهده"}, "private_note": {"اطلاعات محرمانه تازه"}}); code != 200 {
			t.Fatal("dynamic report failed", code, body)
		}
		body, _ := owner.get("/chats/" + cid)
		if !strings.Contains(body, "پیگیری قابل مشاهده") || strings.Contains(body, "اطلاعات محرمانه تازه") {
			t.Fatal("clinical visibility wrong")
		}
		body, _ = admin.get("/management/chats/" + cid)
		if !strings.Contains(body, "اطلاعات محرمانه تازه") {
			t.Fatal("management archive incomplete")
		}
	})
	t.Run("saved answers retain question snapshots", func(t *testing.T) {
		f := a.form(ctx, "signup-owner", nil)
		var uid string
		a.DB.QueryRow(ctx, "SELECT id FROM users WHERE phone='09121112220'").Scan(&uid)
		tx, _ := a.DB.Begin(ctx)
		e := saveFormResponse(ctx, tx, f, uid, uid, map[string]string{"city": "تهران"})
		if e != nil {
			t.Fatal(e)
		}
		tx.Commit(ctx)
		f.Questions[2].Label = "عنوان تازه"
		a.DB.Exec(ctx, "UPDATE business_config SET data=jsonb_set(data,'{questions}',$1) WHERE kind='form' AND id='signup-owner'", mustJSON(f.Questions))
		saved := a.savedForm(ctx, "signup-owner", uid)
		if saved.Values["city"] != "تهران" || saved.Questions[2].Label == "عنوان تازه" {
			t.Fatal("snapshot changed")
		}
		var raw []byte
		a.DB.QueryRow(ctx, "SELECT questions FROM form_responses WHERE scope='signup-owner' AND subject=$1", uid).Scan(&raw)
		var qs []Question
		if json.Unmarshal(raw, &qs) != nil || len(qs) == 0 {
			t.Fatal("snapshot absent")
		}
	})
}
