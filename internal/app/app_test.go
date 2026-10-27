package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRefundPolicy(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name            string
		delta           time.Duration
		exact, platform bool
		want            int64
	}{{"before confirmation", 0, false, false, 1000000}, {"more than six hours", 7 * time.Hour, true, false, 1000000}, {"exactly six hours", 6 * time.Hour, true, false, 1000000}, {"less than six hours", 5 * time.Hour, true, false, 100000}, {"platform fault", time.Hour, true, true, 1000000}} {
		t.Run(tc.name, func(t *testing.T) {
			at := now.Add(tc.delta)
			b := Booking{Amount: 1000000}
			if tc.exact {
				b.ExactTime = &at
			}
			if got := refundable(b, now, tc.platform); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}
func TestNormalization(t *testing.T) {
	if normalize("۰۹۱۲۱۲۳۴۵۶۷") != "09121234567" {
		t.Fatal("Persian phone normalization failed")
	}
	if safeNext("//evil.example") != "/dashboard" || safeNext("/\\evil") != "/dashboard" {
		t.Fatal("open redirect")
	}
	if validMeeting("javascript:alert(1)") || validMeeting("https://user:pass@example.com") {
		t.Fatal("unsafe meeting URL")
	}
}
func TestProductionDisabled(t *testing.T) {
	t.Setenv("APP_MODE", "production")
	if a, e := New(context.Background()); e == nil {
		a.Close()
		t.Fatal("production must refuse development adapters")
	}
}

type browser struct {
	t          *testing.T
	client     *http.Client
	base, csrf string
}

func (b *browser) get(path string) (string, int) {
	b.t.Helper()
	res, err := b.client.Get(b.base + path)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return string(data), res.StatusCode
}
func (b *browser) post(path string, v url.Values) (string, int) {
	b.t.Helper()
	if v == nil {
		v = url.Values{}
	}
	if v.Get("csrf") == "" {
		v.Set("csrf", b.csrf)
	}
	res, err := b.client.PostForm(b.base+path, v)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return string(data), res.StatusCode
}
func field(body, name string) string {
	re := regexp.MustCompile(`name="` + name + `"[^>]*value="([^"]+)"`)
	m := re.FindStringSubmatch(body)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}
func newBrowser(t *testing.T, s *httptest.Server) *browser {
	jar, _ := cookiejar.New(nil)
	b := &browser{t: t, client: &http.Client{Jar: jar}, base: s.URL}
	html, code := b.get("/login")
	if code != 200 {
		t.Fatalf("login %d: %s", code, html)
	}
	b.csrf = field(html, "csrf")
	if b.csrf == "" {
		t.Fatal("missing CSRF")
	}
	return b
}
func (b *browser) login(phone string) {
	b.t.Helper()
	html, code := b.post("/auth/request", url.Values{"phone": {phone}, "next": {"/dashboard"}})
	if code != 200 {
		b.t.Fatalf("request otp %d", code)
	}
	m := regexp.MustCompile(`data-dev-code>([0-9]{6})`).FindStringSubmatch(html)
	if len(m) != 2 {
		b.t.Fatalf("missing dev OTP: %s", html)
	}
	html, code = b.post("/auth/verify", url.Values{"challenge": {field(html, "challenge")}, "code": {m[1]}, "terms": {"on"}, "name": {"آزمایش"}})
	if code != 200 {
		b.t.Fatalf("verify %d: %s", code, html)
	}
	b.csrf = field(html, "csrf")
	if b.csrf == "" {
		b.t.Fatalf("no rotated CSRF: %s", html)
	}
}
func TestIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to an isolated PostgreSQL test database")
	}
	t.Setenv("APP_MODE", "development")
	t.Setenv("DATABASE_URL", dsn)
	a, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	var dbname string
	a.DB.QueryRow(ctx, "SELECT current_database()").Scan(&dbname)
	if !strings.HasSuffix(dbname, "_test") {
		t.Fatal("refusing to reset a database not suffixed _test")
	}
	_, err = a.DB.Exec(ctx, "TRUNCATE users CASCADE")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.seed(ctx); err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(a.Handler())
	defer s.Close()
	owner := newBrowser(t, s)
	owner.login("09121111111")
	ops := newBrowser(t, s)
	ops.login("09120000001")
	vet := newBrowser(t, s)
	vet.login("09120000002")
	groomer := newBrowser(t, s)
	groomer.login("09120000003")
	sampler := newBrowser(t, s)
	sampler.login("09120000004")
	other := newBrowser(t, s)
	other.login("09121111112")
	t.Run("public routes and assets", func(t *testing.T) {
		for _, p := range []string{"/", "/services/vet", "/services/grooming", "/services/sampling", "/join", "/support", "/pages/terms", "/pages/privacy", "/pages/refund", "/pages/telemedicine", "/manifest.webmanifest", "/sw.js", "/static/pets.svg", "/health"} {
			_, code := owner.get(p)
			if code != 200 {
				t.Fatalf("%s: %d", p, code)
			}
		}
	})
	t.Run("csrf and authorization", func(t *testing.T) {
		_, code := owner.post("/book/vet", url.Values{"csrf": {"wrong"}})
		if code != 403 {
			t.Fatalf("csrf %d", code)
		}
		_, code = owner.get("/ops")
		if code != 403 {
			t.Fatalf("owner ops %d", code)
		}
		_, code = owner.get("/admin")
		if code != 403 {
			t.Fatalf("owner admin %d", code)
		}
	})
	ids := map[string]string{}
	slots := map[string]string{}
	for _, svc := range []string{"vet", "grooming", "sampling"} {
		t.Run("booking "+svc, func(t *testing.T) {
			html, code := owner.post("/book/"+svc, nil)
			if code != 200 {
				t.Fatalf("start %d: %s", code, html)
			}
			m := regexp.MustCompile(`/booking/([a-f0-9]{64})/step`).FindStringSubmatch(html)
			if len(m) != 2 {
				t.Fatal("missing draft id")
			}
			// This suite exercises historical paid-before-ops bookings. New request flow has its own integration suite.
			id := m[1]
			if _, e := a.DB.Exec(ctx, "UPDATE bookings SET request_flow=false,time_flow=false,fulfillment_flow=false,lab_flow=false WHERE id=$1", id); e != nil {
				t.Fatal(e)
			}
			ids[svc] = id
			v := url.Values{"step": {"1"}, "pet_name": {"موچی"}, "species": {"cat"}, "age": {"۲ سال"}, "weight": {"۴"}, "symptom": {"digestive"}, "duration": {"today"}, "appetite": {"less"}, "history": {"تست شرح حال"}, "consent": {"on"}, "area": {"vanak"}, "address": {"تهران ونک خیابان نمونه پلاک ۱"}, "sample": {"blood"}}
			if svc == "sampling" {
				bad := url.Values{}
				for k, vs := range v {
					bad[k] = append([]string(nil), vs...)
				}
				bad.Set("sample", "biopsy")
				body, code := owner.post("/booking/"+id+"/step", bad)
				if code != 200 || !strings.Contains(body, "کلینیک") {
					t.Fatal("biopsy guard missing")
				}
				var stage int
				a.DB.QueryRow(ctx, "SELECT stage FROM bookings WHERE id=$1", id).Scan(&stage)
				if stage != 1 {
					t.Fatal("biopsy advanced")
				}
			}
			html, code = owner.post("/booking/"+id+"/step", v)
			if code != 200 {
				t.Fatalf("step1 %d: %s", code, html)
			}
			prov := map[string]string{"vet": "vet-nazanin", "grooming": "groom-sara", "sampling": ""}[svc]
			html, code = owner.post("/booking/"+id+"/step", url.Values{"step": {"2"}, "provider": {prov}})
			if code != 200 {
				t.Fatalf("step2 %d: %s", code, html)
			}
			matches := regexp.MustCompile(`name="slot" value="([^"]+)"`).FindAllStringSubmatch(html, -1)
			m = nil
			tomorrow := time.Now().In(a.Location).AddDate(0, 0, 1).Format("2006-01-02")
			for _, match := range matches {
				var day string
				a.DB.QueryRow(ctx, "SELECT day::text FROM availability WHERE id=$1", match[1]).Scan(&day)
				if day == tomorrow {
					m = match
					break
				}
			}
			if len(m) != 2 {
				t.Fatalf("no available slot: %s", html)
			}
			slots[svc] = m[1]
			html, code = owner.post("/booking/"+id+"/step", url.Values{"step": {"3"}, "slot": {m[1]}, "items": {"bath", "nails"}, "amount": {"1"}})
			if code != 200 {
				t.Fatalf("step3 %d: %s", code, html)
			}
			key := field(html, "payment_key")
			if key == "" {
				t.Fatal("missing payment key")
			}
			var amount int64
			a.DB.QueryRow(ctx, "SELECT amount FROM bookings WHERE id=$1", id).Scan(&amount)
			want := map[string]int64{"vet": 350000, "grooming": 500000, "sampling": 650000}[svc]
			if amount != want {
				t.Fatalf("server price %d want %d", amount, want)
			}
			body, code := owner.post("/booking/"+id+"/pay", url.Values{"payment_key": {key}, "outcome": {"failed"}})
			if code != 200 || !strings.Contains(body, "ناموفق") {
				t.Fatal("failed payment retry not shown")
			}
			html, code = owner.post("/booking/"+id+"/pay", url.Values{"payment_key": {key}, "outcome": {"success"}})
			if code != 200 || !strings.Contains(html, "درخواستت ثبت شد") {
				t.Fatalf("pay %d: %s", code, html)
			}
			owner.post("/booking/"+id+"/pay", url.Values{"payment_key": {key}, "outcome": {"success"}})
			var n int
			a.DB.QueryRow(ctx, "SELECT count(*) FROM booking_events WHERE booking_id=$1 AND event='paid'", id).Scan(&n)
			if n != 1 {
				t.Fatalf("duplicate payment %d", n)
			}
			_, code = other.get("/bookings/" + id)
			if code != 404 {
				t.Fatal("other owner read booking")
			}
		})
	}
	admin := newBrowser(t, s)
	admin.login("09120000005")
	for _, svc := range []string{"vet", "grooming", "sampling"} {
		t.Run("fulfillment "+svc, func(t *testing.T) {
			id := ids[svc]
			pb := map[string]*browser{"vet": vet, "grooming": groomer, "sampling": sampler}[svc]
			jobAction := `action="/provider/bookings/` + id + `"`
			html, code := pb.get("/provider")
			if code != 200 || strings.Contains(html, jobAction) {
				t.Fatalf("paid booking became an actionable provider job: %d", code)
			}
			var day, prd, prov string
			a.DB.QueryRow(ctx, "SELECT day::text,period,provider_id FROM availability WHERE id=$1", slots[svc]).Scan(&day, &prd, &prov)
			h := map[string]int{"morning": 10, "afternoon": 16, "night": 19}[prd]
			exact := fmt.Sprintf("%sT%02d:30", day, h)
			managementAction := "/management/operations/bookings/" + id
			html, code = admin.post(managementAction, url.Values{"action": {"confirm"}})
			notice := regexp.MustCompile(`<div class="alert alert-success" role="status">([^<]+)</div>`).FindStringSubmatch(html)
			if code != 200 || len(notice) != 2 || !strings.Contains(notice[1], "تخصیص") || !strings.Contains(html, "management-body") {
				t.Fatalf("confirmation did not explain the remaining management assignment: %d: %s", code, html)
			}
			var state string
			if err := a.DB.QueryRow(ctx, "SELECT status FROM bookings WHERE id=$1", id).Scan(&state); err != nil || state != "confirmed" {
				t.Fatalf("confirmation bypassed curated assignment: status %q error %v", state, err)
			}
			assignAt := strings.Index(html, `action="`+managementAction+`"`)
			if assignAt < 0 {
				t.Fatal("confirmed booking has no assignment form")
			}
			detailsAt := strings.LastIndex(html[:assignAt], "<details")
			if detailsAt < 0 {
				t.Fatal("confirmed assignment controls are missing")
			}
			endTag := strings.Index(html[detailsAt:], ">")
			if endTag < 0 || !strings.Contains(html[detailsAt:detailsAt+endTag], " open") {
				t.Fatal("confirmed booking assignment controls are collapsed")
			}
			html, code = pb.get("/provider")
			if code != 200 || strings.Contains(html, jobAction) {
				t.Fatalf("confirmation alone exposed an actionable provider job: %d", code)
			}
			html, code = admin.post(managementAction, url.Values{"action": {"assign"}, "provider": {prov}, "exact_time": {exact}, "meeting_url": {"https://meet.example.org/test"}})
			if code != 200 {
				t.Fatalf("assign %d: %s", code, html)
			}
			html, code = pb.get("/provider")
			jobAt := strings.Index(html, jobAction)
			if code != 200 || jobAt < 0 {
				t.Fatalf("assigned booking not visible to its provider: %d", code)
			}
			formEnd := strings.Index(html[jobAt:], "</form>")
			if formEnd < 0 || !strings.Contains(html[jobAt:jobAt+formEnd], `value="accept"`) {
				t.Fatal("assigned provider job has no acceptance action")
			}
			jobsAt, availabilityAt := strings.Index(html, `id="provider-jobs"`), strings.Index(html, `id="provider-availability"`)
			if jobsAt < 0 || availabilityAt < 0 || jobsAt >= availabilityAt {
				t.Fatal("provider jobs are hidden below the availability section")
			}
			if svc == "vet" {
				alternateVet := newBrowser(t, s)
				alternateVet.login("09120000006")
				body, status := alternateVet.get("/provider")
				if status != 200 || strings.Contains(body, jobAction) {
					t.Fatalf("another veterinarian can see the assigned job: %d", status)
				}
				if _, status = alternateVet.post("/provider/bookings/"+id, url.Values{"action": {"accept"}}); status != 404 {
					t.Fatalf("another veterinarian accepted the assigned job: %d", status)
				}
			}
			_, code = other.post("/provider/bookings/"+id, url.Values{"action": {"accept"}})
			if code != 403 {
				t.Fatal("unauthorized provider mutation")
			}
			for _, action := range []string{"accept", "start"} {
				html, code = pb.post("/provider/bookings/"+id, url.Values{"action": {action}})
				if code != 200 {
					t.Fatalf("provider %s %d: %s", action, code, html)
				}
			}
			if svc == "sampling" {
				_, code = ops.post("/ops/bookings/"+id, url.Values{"action": {"result"}, "result": {"too early"}})
				if code != 409 {
					t.Fatal("result accepted before delivery")
				}
				for _, action := range []string{"collected", "delivered"} {
					_, code = pb.post("/provider/bookings/"+id, url.Values{"action": {action}})
					if code != 200 {
						t.Fatal("sample state transition")
					}
				}
				html, code = ops.post("/ops/bookings/"+id, url.Values{"action": {"result"}, "result": {"نتیجه آزمایشی؛ بررسی با دامپزشک"}})
				if code != 200 {
					t.Fatalf("result %d: %s", code, html)
				}
			} else if svc == "vet" {
				var chatID string
				if err := a.DB.QueryRow(ctx, "SELECT id FROM consultation_chats WHERE booking_id=$1", id).Scan(&chatID); err != nil {
					t.Fatal(err)
				}
				html, code = pb.post("/chats/"+chatID+"/end", nil)
				if code != 200 {
					t.Fatalf("end consultation %d: %s", code, html)
				}
				html, code = pb.post("/chats/"+chatID+"/questionnaire", url.Values{"symptoms": {"شرح علائم آزمایشی"}, "diagnosis": {"تشخیص آزمایشی و نیاز به معاینه"}, "prescription": {"خلاصه مشاوره آزمایشی <script>alert(1)</script>"}})
				if code != 200 {
					t.Fatalf("clinical questionnaire %d: %s", code, html)
				}
				html, code = owner.post("/chats/"+chatID+"/questionnaire", url.Values{"rating": {"5"}, "communication": {"5"}, "clarity": {"4"}, "body": {"تجربه خوب"}})
				if code != 200 {
					t.Fatalf("customer questionnaire %d: %s", code, html)
				}
			} else {
				html, code = pb.post("/provider/bookings/"+id, url.Values{"action": {"complete"}, "prescription": {"خلاصه مشاوره آزمایشی <script>alert(1)</script>"}})
				if code != 200 {
					t.Fatalf("complete %d: %s", code, html)
				}
			}
			body, code := owner.get("/bookings/" + id)
			if code != 200 || svc == "vet" && strings.Contains(body, "<script>alert(1)</script>") {
				t.Fatal("escaped document output")
			}
			_, code = owner.post("/bookings/"+id+"/review", url.Values{"rating": {"5"}, "body": {"تجربه خوب"}})
			if code != 200 {
				t.Fatal("review not accepted")
			}
		})
	}
	t.Run("otp replay expiration and attempts", func(t *testing.T) {
		b := newBrowser(t, s)
		body, _ := b.post("/auth/request", url.Values{"phone": {"09121111113"}})
		challenge := field(body, "challenge")
		a.DB.Exec(ctx, "UPDATE otp_challenges SET attempts=5 WHERE id=$1", challenge)
		body, _ = b.post("/auth/verify", url.Values{"challenge": {challenge}, "code": {"000000"}, "terms": {"on"}})
		if !strings.Contains(body, "منقضی") {
			t.Fatal("attempt cap missing")
		}
		a.DB.Exec(ctx, "UPDATE otp_challenges SET attempts=0,expires_at=now()-interval '1 minute' WHERE id=$1", challenge)
		body, _ = b.post("/auth/verify", url.Values{"challenge": {challenge}, "code": {"000000"}, "terms": {"on"}})
		if !strings.Contains(body, "منقضی") {
			t.Fatal("expiry missing")
		}
		a.DB.Exec(ctx, "UPDATE otp_challenges SET expires_at=now()+interval '1 minute',consumed=true WHERE id=$1", challenge)
		body, _ = b.post("/auth/verify", url.Values{"challenge": {challenge}, "code": {"000000"}, "terms": {"on"}})
		if !strings.Contains(body, "مصرف") {
			t.Fatal("replay accepted")
		}
	})
	t.Run("ticket isolation and reply", func(t *testing.T) {
		body, code := owner.post("/support", url.Values{"email": {"test@example.com"}, "subject": {"پیگیری رزرو"}, "body": {"می‌خواهم پیگیری کنم"}})
		if code != 200 || !strings.Contains(body, "پیگیری رزرو") {
			t.Fatal("ticket missing")
		}
		var id string
		a.DB.QueryRow(ctx, "SELECT id FROM tickets WHERE subject='پیگیری رزرو'").Scan(&id)
		ops.post("/ops/tickets/"+id, url.Values{"reply": {"پاسخ عملیات"}})
		body, _ = owner.get("/support")
		if !strings.Contains(body, "پاسخ عملیات") {
			t.Fatal("reply missing")
		}
		body, _ = other.get("/support")
		if strings.Contains(body, "پاسخ عملیات") {
			t.Fatal("ticket leak")
		}
	})
	t.Run("provider application requires verification", func(t *testing.T) {
		body, code := other.post("/join", url.Values{"service": {"vet"}, "name": {"دامپزشک تست"}, "license": {"TEST-100"}, "bio": {"تجربه دامپزشکی"}, "price": {"350000"}, "area": {"vanak"}})
		if code != 200 || !strings.Contains(body, "در انتظار") {
			t.Fatal("application missing")
		}
		var id string
		a.DB.QueryRow(ctx, "SELECT id FROM applications WHERE name='دامپزشک تست'").Scan(&id)
		_, code = ops.post("/ops/applications/"+id, url.Values{"decision": {"approved"}})
		if code != 400 {
			t.Fatal("verification gate absent")
		}
		_, code = ops.post("/ops/applications/"+id, url.Values{"decision": {"approved"}, "verified": {"on"}})
		if code != 200 {
			t.Fatalf("approval %d", code)
		}
	})
	fixture := func(st string, exact *time.Time) string {
		id := token()
		var uid string
		a.DB.QueryRow(ctx, "SELECT id FROM users WHERE phone='09121111111'").Scan(&uid)
		cookieURL, _ := url.Parse(s.URL)
		session := owner.client.Jar.Cookies(cookieURL)[0].Value
		_, err := a.DB.Exec(ctx, "INSERT INTO bookings(id,session_token,owner_id,service,stage,pet_name,provider_id,amount,payment_key,status,payment_status,paid_at,exact_time) VALUES($1,$2,$3,'vet',4,'fixture','vet-nazanin',350000,$4,$5,'paid',now(),$6)", id, session, uid, token(), st, exact)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	t.Run("vet replacement requires customer consent", func(t *testing.T) {
		id := fixture("paid", nil)
		_, code := ops.post("/ops/bookings/"+id, url.Values{"action": {"assign"}, "provider": {"vet-arman"}, "exact_time": {"2030-01-01T10:00"}})
		if code != 409 {
			t.Fatal("silent vet swap accepted")
		}
		_, code = ops.post("/ops/bookings/"+id, url.Values{"action": {"replace"}, "provider": {"vet-arman"}})
		if code != 200 {
			t.Fatalf("replacement %d", code)
		}
		var prov, st string
		a.DB.QueryRow(ctx, "SELECT provider_id,status FROM bookings WHERE id=$1", id).Scan(&prov, &st)
		if prov != "vet-nazanin" || st != "replacement_pending" {
			t.Fatal("provider swapped before consent")
		}
		body, _ := owner.get("/bookings/" + id)
		if !strings.Contains(body, "پذیرش جایگزین") {
			t.Fatal("missing consent UI")
		}
		_, code = owner.post("/bookings/"+id+"/replacement", nil)
		if code != 200 {
			t.Fatal("consent failed")
		}
		a.DB.QueryRow(ctx, "SELECT provider_id,status FROM bookings WHERE id=$1", id).Scan(&prov, &st)
		if prov != "vet-arman" || st != "paid" {
			t.Fatal("accepted replacement not pending coordination")
		}
	})
	t.Run("cancellation refund policy persisted", func(t *testing.T) {
		at := time.Now().Add(time.Hour)
		for _, tc := range []struct {
			status string
			exact  *time.Time
			want   int64
		}{{"paid", nil, 350000}, {"assigned", &at, 35000}} {
			id := fixture(tc.status, tc.exact)
			_, code := owner.post("/bookings/"+id+"/cancel", url.Values{"reason": {"تغییر برنامه"}})
			if code != 200 {
				t.Fatal("cancel failed")
			}
			var amount int64
			var st string
			a.DB.QueryRow(ctx, "SELECT refund_amount,status FROM bookings WHERE id=$1", id).Scan(&amount, &st)
			if amount != tc.want || st != "cancel_requested" {
				t.Fatalf("refund %d state %s", amount, st)
			}
			_, code = ops.post("/ops/bookings/"+id, url.Values{"action": {"refund"}})
			if code != 200 {
				t.Fatal("ops refund failed")
			}
			a.DB.QueryRow(ctx, "SELECT status FROM bookings WHERE id=$1", id).Scan(&st)
			if st != "cancelled" {
				t.Fatal("refund did not close case")
			}
		}
	})
	t.Run("platform failure returns full amount", func(t *testing.T) {
		at := time.Now().Add(time.Hour)
		id := fixture("assigned", &at)
		_, code := ops.post("/ops/bookings/"+id, url.Values{"action": {"platform_cancel"}, "notes": {"عدم دسترسی ارائه‌دهنده؛ بررسی جبران"}})
		if code != 200 {
			t.Fatal("platform cancel failed")
		}
		var amount int64
		a.DB.QueryRow(ctx, "SELECT refund_amount FROM bookings WHERE id=$1", id).Scan(&amount)
		if amount != 350000 {
			t.Fatal("platform refund not full")
		}
	})

	t.Run("capacity race", func(t *testing.T) {
		day := time.Now().In(a.Location).AddDate(0, 0, 3).Format("2006-01-02")
		slot := token()
		a.DB.Exec(ctx, "INSERT INTO availability(id,provider_id,day,period,capacity) VALUES($1,'vet-nazanin',$2,'noon',1)", slot, day)
		var ids []string
		var keys []string
		for i := 0; i < 2; i++ {
			id, key := token(), token()
			ids = append(ids, id)
			keys = append(keys, key)
			var ownerID, session string
			a.DB.QueryRow(ctx, "SELECT id FROM users WHERE phone='09121111111'").Scan(&ownerID)
			cookieURL, _ := url.Parse(s.URL)
			session = owner.client.Jar.Cookies(cookieURL)[0].Value
			a.DB.Exec(ctx, "INSERT INTO bookings(id,session_token,owner_id,service,stage,pet_name,provider_id,availability_id,amount,payment_key) VALUES($1,$2,$3,'vet',4,'race','vet-nazanin',$4,350000,$5)", id, session, ownerID, slot, key)
			stampTestQuote(t, a, id)
		}
		var wg sync.WaitGroup
		codes := make(chan int, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				res, err := owner.client.PostForm(s.URL+"/booking/"+ids[i]+"/pay", url.Values{"csrf": {owner.csrf}, "payment_key": {keys[i]}, "outcome": {"success"}})
				if err != nil {
					codes <- 0
					return
				}
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
				codes <- res.StatusCode
			}(i)
		}
		wg.Wait()
		close(codes)
		success, conflict := 0, 0
		for c := range codes {
			if c == 200 {
				success++
			}
			if c == 409 {
				conflict++
			}
		}
		if success != 1 || conflict != 1 {
			t.Fatalf("expected one payment and one capacity conflict, got %d and %d", success, conflict)
		}
	})
}
