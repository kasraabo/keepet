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
	"sync"
	"testing"
)

func TestServiceFulfillment(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	u, err := url.Parse(dsn)
	if err != nil || !strings.HasSuffix(u.Path, "_test") {
		t.Fatal("test database required")
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
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := a.DB.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("TRUNCATE users CASCADE;TRUNCATE business_config;TRUNCATE fulfillment_settings;INSERT INTO fulfillment_settings(id) VALUES(1);TRUNCATE referral_settings;INSERT INTO referral_settings(id,enabled,provider_enabled,invitee_bonus_enabled,invitee_bonus,vet_bps,grooming_bps,sampling_bps,basis) VALUES(1,true,true,true,200000,1000,1000,1000,'platform_share')")
	if err = a.seed(ctx); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	actor := func(phone string) *browser { b := newBrowser(t, srv); b.login(phone); return b }
	owner := actor("09129992220")
	other := actor("09129992221")
	groom := actor("09120000003")
	sampler := actor("09120000004")
	ops := actor("09120000001")
	admin := actor("09120000005")
	vet := actor("09120000002")
	var uid, session, parent string
	a.DB.QueryRow(ctx, "SELECT id FROM users WHERE phone='09129992220'").Scan(&uid)
	a.DB.QueryRow(ctx, "SELECT token FROM sessions WHERE user_id=$1 LIMIT 1", uid).Scan(&session)
	a.DB.QueryRow(ctx, "SELECT id FROM users WHERE phone='09129992221'").Scan(&parent)
	rc, err := a.ensureReferralCode(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"demo-groomer", "demo-sampler", "demo-vet"} {
		exec("SELECT referral_register($1,$2)", p, rc.Code)
	}
	post := func(b *browser, path string, v url.Values, want int) string {
		t.Helper()
		h, c := b.post(path, v)
		if c != want {
			t.Fatalf("%s got %d want %d %.1600s", path, c, want, h[max(0, strings.Index(h, "<main")):])
		}
		return h
	}
	action := func(b *browser, id, act string, want int) {
		t.Helper()
		post(b, "/bookings/"+id+"/fulfillment", url.Values{"action": {act}}, want)
	}
	scalar := func(q string, args ...any) int64 {
		t.Helper()
		var n int64
		if e := a.DB.QueryRow(ctx, q, args...).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	income := func(id string) int64 {
		return scalar("SELECT count(*) FROM wallet_entries WHERE booking_id=$1 AND kind='service_income'", id)
	}
	awards := func(id string) int64 {
		return scalar("SELECT count(*) FROM referral_order_awards WHERE booking_id=$1 AND status='credited'", id)
	}
	state := func(id string) string {
		var s string
		if e := a.DB.QueryRow(ctx, "SELECT status FROM bookings WHERE id=$1", id).Scan(&s); e != nil {
			t.Fatal(e)
		}
		return s
	}
	fixture := func(svc string) string {
		t.Helper()
		id := token()
		p := map[string]string{"grooming": "groom-sara", "sampling": "sample-amin", "vet": "vet-nazanin"}[svc]
		exec("INSERT INTO bookings(id,session_token,owner_id,service,stage,pet_name,species,provider_id,payment_key,fulfillment_flow,amount,provider_due,commission_bps,status) VALUES($1,$2,$3,$4,4,'موچی','cat',$5,$6,true,1000000,800000,2000,'accepted')", id, session, uid, svc, p, token())
		exec("UPDATE bookings SET payment_status='paid',paid_at=now() WHERE id=$1", id)
		return id
	}
	path := func(id string) string { return "/bookings/" + id + "/fulfillment" }
	code := func(id string) string {
		t.Helper()
		h, c := owner.get(path(id))
		if c != 200 || !strings.Contains(h, "data-arrival-code") {
			t.Fatalf("owner code missing %d", c)
		}
		var raw []byte
		a.DB.QueryRow(ctx, "SELECT code_cipher FROM arrival_challenges WHERE booking_id=$1", id).Scan(&raw)
		s, e := openIntegration("arrival:"+id, raw)
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(raw, []byte(s)) {
			t.Fatal("plaintext OTP persisted")
		}
		return s
	}
	arrive := func(id string, b *browser) {
		t.Helper()
		action(b, id, "arrived", 200)
		post(b, path(id), url.Values{"action": {"verify_arrival"}, "code": {code(id)}}, 200)
		if state(id) != "in_progress" {
			t.Fatal("arrival state")
		}
	}
	upload := func(id string, b *browser, data []byte, want int) string {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, v := range map[string]string{"csrf": b.csrf, "action": "upload_receipt", "lab_name": "آزمایشگاه تست", "reference": "۱۲۳"} {
			if e := mw.WriteField(k, v); e != nil {
				t.Fatal(e)
			}
		}
		f, e := mw.CreateFormFile("receipt", "receipt.png")
		if e != nil {
			t.Fatal(e)
		}
		f.Write(data)
		mw.Close()
		h, c, _ := chatRequest(t, b, "POST", path(id), mw.FormDataContentType(), &buf)
		if c != want {
			t.Fatalf("upload %d want %d %.1200s", c, want, h)
		}
		var rid string
		a.DB.QueryRow(ctx, "SELECT COALESCE(handover_receipt_id,'') FROM bookings WHERE id=$1", id).Scan(&rid)
		return rid
	}
	review := func(id, rid, act string, b *browser, want int) {
		t.Helper()
		post(b, path(id), url.Values{"action": {act}, "receipt_id": {rid}, "reason": {"توضیح بررسی رسید"}}, want)
	}
	t.Run("role boundaries code lifecycle and attendance evidence", func(t *testing.T) {
		id := fixture("grooming")
		if _, c := other.get(path(id)); c != 403 {
			t.Fatal("unrelated read", c)
		}
		action(other, id, "arrived", 403)
		action(owner, id, "arrived", 403)
		action(ops, id, "arrived", 403)
		action(sampler, id, "arrived", 403)
		action(groom, id, "groom_done", 409)
		post(groom, "/provider/bookings/"+id, url.Values{"action": {"start"}}, 409)
		if _, e := a.DB.Exec(ctx, "UPDATE bookings SET status='completed' WHERE id=$1", id); e == nil {
			t.Fatal("SQL completion bypass")
		}
		action(groom, id, "arrived", 200)
		old := code(id)
		for _, b := range []*browser{groom, ops, admin} {
			h, c := b.get(path(id))
			if c != 200 || strings.Contains(h, "data-arrival-code") {
				t.Fatal("code leaked", c)
			}
		}
		if scalar("SELECT count(*) FROM outbox WHERE booking_id=$1 AND payload::text LIKE '%'||$2||'%'", id, old) != 0 {
			t.Fatal("OTP leaked to outbox")
		}
		action(groom, id, "arrived", 429)
		for i := 0; i < 5; i++ {
			post(groom, path(id), url.Values{"action": {"verify_arrival"}, "code": {"wrong"}}, 400)
		}
		if scalar("SELECT attempts FROM arrival_challenges WHERE booking_id=$1", id) != 5 {
			t.Fatal("attempts rolled back")
		}
		post(groom, path(id), url.Values{"action": {"verify_arrival"}, "code": {old}}, 409)
		exec("UPDATE arrival_challenges SET last_sent_at=now()-interval '1 minute' WHERE booking_id=$1", id)
		action(groom, id, "arrived", 200)
		fresh := code(id)
		if fresh == old {
			t.Fatal("resend repeated the previous visible code")
		}
		post(groom, path(id), url.Values{"action": {"verify_arrival"}, "code": {old}}, 400)
		// The valid new code also stops working when its expiry passes.
		exec("UPDATE arrival_challenges SET expires_at=now()-interval '1 second' WHERE booking_id=$1", id)
		post(groom, path(id), url.Values{"action": {"verify_arrival"}, "code": {fresh}}, 409)
		exec("UPDATE arrival_challenges SET last_sent_at=now()-interval '1 minute' WHERE booking_id=$1", id)
		action(groom, id, "arrived", 200)
		post(groom, path(id), url.Values{"action": {"verify_arrival"}, "code": {code(id)}}, 200)
		action(groom, id, "verify_arrival", 200)
		if income(id) != 0 || awards(id) != 0 {
			t.Fatal("income before completion")
		}
		if _, e := a.DB.Exec(ctx, "UPDATE bookings SET attendance_verified_at=NULL WHERE id=$1", id); e == nil {
			t.Fatal("attendance mutable")
		}
		if _, e := a.DB.Exec(ctx, "UPDATE arrival_challenges SET consumed_at=NULL WHERE booking_id=$1", id); e == nil {
			t.Fatal("challenge mutable")
		}
	})
	t.Run("groom customer confirmation is once only", func(t *testing.T) {
		id := fixture("grooming")
		arrive(id, groom)
		action(groom, id, "groom_done", 200)
		if income(id) != 0 {
			t.Fatal("early income")
		}
		action(groom, id, "confirm_done", 403)
		action(other, id, "confirm_done", 403)
		action(owner, id, "confirm_done", 200)
		action(owner, id, "confirm_done", 200)
		exec("UPDATE bookings SET status=status WHERE id=$1", id)
		if income(id) != 1 || awards(id) != 1 {
			t.Fatal("missing or double credit", income(id), awards(id))
		}
		if scalar("SELECT amount FROM referral_order_awards WHERE booking_id=$1", id) != 20000 {
			t.Fatal("referral not ten percent of platform share")
		}
	})
	t.Run("dispute stops automatic completion; resolution restarts review window", func(t *testing.T) {
		id := fixture("grooming")
		arrive(id, groom)
		action(groom, id, "groom_done", 200)
		exec("UPDATE bookings SET completion_deadline=now()-interval '1 minute' WHERE id=$1", id)
		post(owner, path(id), url.Values{"action": {"report_issue"}, "reason": {"خدمت کامل انجام نشده"}}, 200)
		if e := a.reconcileFulfillments(ctx); e != nil {
			t.Fatal(e)
		}
		if income(id) != 0 {
			t.Fatal("disputed income")
		}
		action(owner, id, "confirm_done", 409)
		post(ops, path(id), url.Values{"action": {"resolve_continue"}, "reason": {"مشکل با هماهنگی رفع شد"}}, 200)
		if scalar("SELECT count(*) FROM bookings WHERE id=$1 AND completion_deadline>now()+interval '23 hour'", id) != 1 {
			t.Fatal("no fresh review window")
		}
		exec("UPDATE bookings SET completion_deadline=now()-interval '1 second' WHERE id=$1", id)
		var wg sync.WaitGroup
		errs := make(chan error, 3)
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); errs <- a.reconcileFulfillments(ctx) }()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		if income(id) != 1 || awards(id) != 1 {
			t.Fatal("automatic completion duplicate or missing")
		}
	})
	t.Run("no show resolves to full wallet refund without specialist income", func(t *testing.T) {
		id := fixture("sampling")
		post(owner, path(id), url.Values{"action": {"report_issue"}, "reason": {"متخصص مراجعه نکرده است"}}, 200)
		action(sampler, id, "arrived", 409)
		post(ops, path(id), url.Values{"action": {"resolve_refund"}, "reason": {"عدم حضور تأیید شد"}}, 200)
		post(ops, "/ops/bookings/"+id, url.Values{"action": {"refund"}}, 200)
		if state(id) != "cancelled" || income(id) != 0 || awards(id) != 0 {
			t.Fatal("no-show state/income")
		}
		if scalar("SELECT cash_delta FROM wallet_entries WHERE booking_id=$1 AND kind='refund'", id) != 1000000 {
			t.Fatal("full refund not posted")
		}
	})
	t.Run("handover versions privacy and independent result publication", func(t *testing.T) {
		id := fixture("sampling")
		upload(id, sampler, chatPNG(t), 409)
		arrive(id, sampler)
		action(sampler, id, "collected", 200)
		upload(id, owner, chatPNG(t), 403)
		upload(id, sampler, []byte("<script>fake image</script>"), 400)
		upload(id, sampler, make([]byte, (8<<20)+1), 400)
		post(ops, "/ops/bookings/"+id, url.Values{"action": {"result"}, "result": {"انتشار زودهنگام"}}, 409)
		rid := upload(id, sampler, chatPNG(t), 200)
		review(id, rid, "approve_receipt", sampler, 403)
		review(id, rid, "reject_receipt", ops, 200)
		rid2 := upload(id, sampler, chatPNG(t), 200)
		if rid2 == rid {
			t.Fatal("receipt overwritten")
		}
		review(id, rid, "approve_receipt", ops, 409)
		for _, b := range []*browser{owner, other, groom} {
			_, c, _ := chatRequest(t, b, "GET", "/bookings/"+id+"/handover/"+rid2, "", nil)
			if c != 403 {
				t.Fatal("receipt access leaked", c)
			}
		}
		for _, b := range []*browser{sampler, ops} {
			_, c, h := chatRequest(t, b, "GET", "/bookings/"+id+"/handover/"+rid2, "", nil)
			if c != 200 || !strings.Contains(h.Get("Content-Disposition"), "attachment") || h.Get("Cache-Control") != "private, no-store" {
				t.Fatal("receipt download", c, h)
			}
		}
		if income(id) != 0 || awards(id) != 0 {
			t.Fatal("unreviewed receipt credited")
		}
		review(id, rid2, "approve_receipt", ops, 200)
		if state(id) != "delivered" || income(id) != 1 || awards(id) != 1 {
			t.Fatal("handover not settled")
		}
		review(id, rid2, "approve_receipt", ops, 200)
		post(sampler, "/ops/bookings/"+id, url.Values{"action": {"result"}, "result": {"غیر مجاز"}}, 403)
		post(ops, "/ops/bookings/"+id, url.Values{"action": {"result"}, "result": {"نتیجه معتبر آزمایشگاه"}}, 200)
		if income(id) != 1 || awards(id) != 1 || state(id) != "result_ready" {
			t.Fatal("result double settlement")
		}
		if _, e := a.DB.Exec(ctx, "UPDATE handover_receipts SET state='pending' WHERE id=$1", rid2); e == nil {
			t.Fatal("review mutable")
		}
		if h, c := owner.get("/bookings/" + id); c != 200 || !strings.Contains(h, "نتیجه معتبر آزمایشگاه") {
			t.Fatal("result unavailable")
		}
	})
	t.Run("vet expiry waits for clinical report; prescription independent of survey", func(t *testing.T) {
		id := fixture("vet")
		post(vet, "/provider/bookings/"+id, url.Values{"action": {"start"}}, 200)
		var cid string
		a.DB.QueryRow(ctx, "SELECT id FROM consultation_chats WHERE booking_id=$1", id).Scan(&cid)
		exec("UPDATE consultation_chats SET expires_at=now()-interval '1 second' WHERE id=$1", cid)
		if e := a.reconcileChats(ctx); e != nil {
			t.Fatal(e)
		}
		if state(id) != "awaiting_report" || income(id) != 0 || awards(id) != 0 {
			t.Fatal("expiry completion", state(id))
		}
		post(vet, "/chats/"+cid+"/questionnaire", url.Values{"symptoms": {"بی حالی"}, "diagnosis": {"نیاز به معاینه"}, "prescription": {"نسخه روشن برای مشتری"}}, 200)
		if state(id) != "completed" || income(id) != 1 || awards(id) != 1 {
			t.Fatal("clinical completion")
		}
		if h, c := owner.get("/bookings/" + id); c != 200 || !strings.Contains(h, "نسخه روشن برای مشتری") {
			t.Fatal("prescription gated by survey")
		}
	})
	t.Run("administrator settings revision and Persian numbers", func(t *testing.T) {
		if _, c := ops.get("/management/fulfillment"); c != 403 {
			t.Fatal("settings role")
		}
		h, c := admin.get("/management/fulfillment")
		if c != 200 {
			t.Fatal("settings page", c)
		}
		rev := field(h, "revision")
		values := url.Values{"revision": {rev}, "arrival_minutes": {"۵"}, "arrival_attempts": {"۳"}, "resend_seconds": {"۳۰"}, "hourly_limit": {"۶"}, "completion_hours": {"۴۸"}}
		post(admin, "/management/fulfillment", values, 200)
		post(admin, "/management/fulfillment", values, 409)
		if scalar("SELECT completion_hours FROM fulfillment_settings") != 48 {
			t.Fatal("settings not persisted")
		}
	})
	for _, p := range []string{"/provider", "/ops", "/management", "/management/finance", "/management/referrals"} {
		b := admin
		if p == "/provider" {
			b = groom
		}
		if h, c := b.get(p); c != 200 {
			t.Fatalf("panel %s %d %.400s", p, c, h)
		}
	}
}
