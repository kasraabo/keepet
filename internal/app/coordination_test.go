package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCoordinationNotifications(t *testing.T) {
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
	owner := actor("09129994440")
	other := actor("09129994441")
	groom := actor("09120000003")
	sampler := actor("09120000004")
	ops := actor("09120000001")
	admin := actor("09120000005")
	var uid, session, gid string
	a.DB.QueryRow(ctx, "SELECT id FROM users WHERE phone='09129994440'").Scan(&uid)
	a.DB.QueryRow(ctx, "SELECT token FROM sessions WHERE user_id=$1 LIMIT 1", uid).Scan(&session)
	a.DB.QueryRow(ctx, "SELECT id FROM users WHERE phone='09120000003'").Scan(&gid)
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
		h, c := b.post(p, v)
		if c != want {
			t.Fatalf("%s %d want %d %.1200s", p, c, want, h[max(0, strings.Index(h, "<main")):])
		}
		return h
	}
	fixture := func(svc string) (string, string) {
		t.Helper()
		id := token()
		provider := map[string]string{"grooming": "groom-sara", "sampling": "sample-amin"}[svc]
		exec(`INSERT INTO bookings(id,session_token,owner_id,service,provider_id,pet_name,species,payment_key,request_flow,status,stage,amount,provider_due) VALUES($1,$2,$3,$4,$5,'موچی','cat',$6,true,'awaiting_payment',4,1000000,800000)`, id, session, uid, svc, provider, token())
		if scalar("SELECT count(*) FROM consultation_chats WHERE booking_id=$1", id) != 0 {
			t.Fatal("room before payment")
		}
		exec("UPDATE bookings SET payment_status='paid',status='accepted',paid_at=now() WHERE id=$1", id)
		var cid string
		if e := a.DB.QueryRow(ctx, "SELECT id FROM consultation_chats WHERE booking_id=$1", id).Scan(&cid); e != nil {
			t.Fatal(e)
		}
		return id, cid
	}
	send := func(b *browser, cid, body string, want int) {
		t.Helper()
		post(b, "/chats/"+cid+"/messages", url.Values{"text": {body}}, want)
	}
	read := func(b *browser, cid string, id int64, want int) {
		t.Helper()
		post(b, "/chats/"+cid+"/read", url.Values{"last_message_id": {fmt.Sprint(id)}}, want)
	}
	bid, cid := fixture("grooming")
	_, sampleCID := fixture("sampling")
	t.Run("automatic paid room and role-specific controls", func(t *testing.T) {
		for _, b := range []*browser{owner, groom, ops} {
			h, c := b.get("/chats/" + cid)
			if c != 200 || !strings.Contains(h, "هماهنگی خدمت") || strings.Contains(h, "data-chat-end") || strings.Contains(h, "data-chat-leave") || strings.Contains(h, "class=\"chat-admin-duration\"") {
				t.Fatal("home controls", c)
			}
		}
		for _, b := range []*browser{other, sampler} {
			if _, c := b.get("/chats/" + cid); c != 404 {
				t.Fatal("foreign room", c)
			}
			send(b, cid, "غیر مجاز", 404)
		}
		send(ops, cid, "غیر مجاز", 403)
		for _, b := range []*browser{owner, groom} {
			post(b, "/chats/"+cid+"/end", nil, 409)
			if _, c := b.get("/chats/" + cid + "/questionnaire"); c != 409 {
				t.Fatal("medical GET on home", c)
			}
			post(b, "/chats/"+cid+"/questionnaire", url.Values{"symptoms": {"بایستی رد شود"}}, 409)
		}
		post(admin, "/management/chats/"+cid+"/duration", url.Values{"duration_minutes": {"60"}}, 409)
		exec("UPDATE bookings SET notes='replay' WHERE id=$1", bid)
		if scalar("SELECT count(*) FROM consultation_chats WHERE booking_id=$1", bid) != 1 {
			t.Fatal("duplicate room")
		}
	})
	t.Run("unread cursor and notification privacy", func(t *testing.T) {
		send(owner, cid, "پیام اول هماهنگی", 200)
		first := scalar("SELECT max(id) FROM consultation_messages WHERE chat_id=$1", cid)
		send(owner, cid, "پیام دوم هماهنگی", 200)
		second := scalar("SELECT max(id) FROM consultation_messages WHERE chat_id=$1", cid)
		if scalar("SELECT count(*) FROM notifications WHERE user_id=$1 AND chat_id=$2 AND read_at IS NULL", gid, cid) != 2 {
			t.Fatal("message notifications missing")
		}
		h, c := groom.get("/provider")
		if c != 200 || !strings.Contains(h, "۲ پیام خوانده‌نشده") {
			t.Fatal("summary unread", c)
		}
		if scalar("SELECT count(*) FROM notifications WHERE chat_id=$1 AND user_id=$2", cid, uid) != 0 {
			t.Fatal("sender notified")
		}
		h, c = groom.get("/notifications")
		if c != 200 || strings.Contains(h, "پیام اول هماهنگی") {
			t.Fatal("message content leaked into notification")
		}
		// Acknowledging an older rendered snapshot must not read a later arrival.
		read(groom, cid, first, 200)
		if scalar("SELECT last_message_id FROM chat_reads WHERE chat_id=$1 AND user_id=$2", cid, gid) != first {
			t.Fatal("cursor")
		}
		if scalar("SELECT count(*) FROM notifications WHERE user_id=$1 AND chat_id=$2 AND read_at IS NULL", gid, cid) != 1 {
			t.Fatal("read unseen message")
		}
		read(groom, cid, second, 200)
		read(groom, cid, first, 200)
		if scalar("SELECT last_message_id FROM chat_reads WHERE chat_id=$1 AND user_id=$2", cid, gid) != second {
			t.Fatal("cursor moved backwards")
		}
		read(other, cid, second, 404)
		read(ops, cid, second, 403)
		send(owner, sampleCID, "سفارش دیگر", 200)
		foreign := scalar("SELECT max(id) FROM consultation_messages WHERE chat_id=$1", sampleCID)
		read(groom, cid, foreign, 400)
		if _, c := other.get("/notifications"); c != 200 {
			t.Fatal(c)
		}
		note := scalar("SELECT max(id) FROM notifications WHERE chat_id=$1 AND user_id=$2", cid, gid)
		post(other, "/notifications/"+fmt.Sprint(note)+"/read", nil, 404)
		var state map[string]any
		body, code, _ := chatRequest(t, groom, "GET", "/chats/"+cid+"/messages", "", nil)
		if code != 200 || json.Unmarshal([]byte(body), &state) != nil {
			t.Fatal("poll", code)
		}
		send(groom, cid, "پاسخ گرومر", 200)
	})
	t.Run("private media retains existing authorization", func(t *testing.T) {
		_, c, _ := chatMultipart(t, owner, cid, owner.csrf, "تصویر هماهنگی", "cat.png", "image/png", chatPNG(t))
		if c != 200 {
			t.Fatal("upload", c)
		}
		var file string
		a.DB.QueryRow(ctx, "SELECT f.id FROM consultation_attachments f JOIN consultation_messages m ON m.id=f.message_id WHERE m.chat_id=$1 ORDER BY m.id DESC LIMIT 1", cid).Scan(&file)
		for _, b := range []*browser{owner, groom, ops} {
			_, c, _ := chatRequest(t, b, "GET", "/chat-media/"+file, "", nil)
			if c != 200 {
				t.Fatal("media", c)
			}
		}
		_, c, _ = chatRequest(t, other, "GET", "/chat-media/"+file, "", nil)
		if c != 404 {
			t.Fatal("foreign media", c)
		}
	})
	t.Run("gated requests are hidden from provider; operations receive the event", func(t *testing.T) {
		id := token()
		exec(`INSERT INTO bookings(id,session_token,owner_id,service,provider_id,pet_name,payment_key,request_flow,status) VALUES($1,$2,$3,'grooming','groom-sara','پت', $4,true,'pending_ops')`, id, session, uid, token())
		exec("INSERT INTO booking_events(booking_id,actor_id,event,detail) VALUES($1,$2,'pending_ops','اطلاعات خصوصی اپراتور')", id, uid)
		if scalar("SELECT count(*) FROM notifications WHERE booking_id=$1 AND user_id=$2", id, gid) != 0 {
			t.Fatal("gate leak")
		}
		if scalar("SELECT count(*) FROM notifications n JOIN users u ON u.id=n.user_id WHERE n.booking_id=$1 AND u.role IN ('ops','admin')", id) != 2 {
			t.Fatal("ops event missing")
		}
		exec("UPDATE bookings SET status='pending_provider' WHERE id=$1", id)
		exec("INSERT INTO booking_events(booking_id,actor_id,event,detail) VALUES($1,'demo-ops','pending_provider','متن محرمانه')", id)
		if scalar("SELECT count(*) FROM notifications WHERE booking_id=$1 AND user_id=$2", id, gid) != 1 {
			t.Fatal("provider gate approval notice")
		}
		exec("INSERT INTO outbox(booking_id,user_id,kind,dedup_key,payload) VALUES($1,$2,'provider_response_reminder',$3,'{}')", id, gid, token())
		if scalar("SELECT count(*) FROM notifications WHERE booking_id=$1 AND user_id=$2 AND event='provider_response_reminder'", id, gid) != 1 {
			t.Fatal("reminder notification missing")
		}
		h, _ := groom.get("/notifications")
		if strings.Contains(h, "متن محرمانه") {
			t.Fatal("event detail leaked")
		}
	})
	t.Run("snapshot read-all preserves newer notifications", func(t *testing.T) {
		before := scalar("SELECT COALESCE(max(id),0) FROM notifications WHERE user_id=$1", uid)
		send(groom, cid, "پیام پس از بازشدن فهرست", 200)
		post(owner, "/notifications/all/read", url.Values{"before_id": {fmt.Sprint(before)}}, 200)
		if scalar("SELECT count(*) FROM notifications WHERE user_id=$1 AND id>$2 AND read_at IS NULL", uid, before) != 1 {
			t.Fatal("new notice marked read")
		}
	})
	t.Run("groom completion closes without changing income; races cannot add late messages", func(t *testing.T) {
		exec("UPDATE bookings SET status='completed' WHERE id=$1", bid)
		if scalar("SELECT count(*) FROM consultation_chats WHERE id=$1 AND closed_at IS NOT NULL", cid) != 1 {
			t.Fatal("room not archived")
		}
		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); a.reconcileChats(ctx) }()
		}
		wg.Wait()
		send(owner, cid, "پیام دیرهنگام", 409)
		send(groom, cid, "پیام دیرهنگام", 409)
		if scalar("SELECT count(*) FROM consultation_customer_forms WHERE chat_id=$1", cid) != 0 {
			t.Fatal("medical survey created")
		}
		if h, c := owner.get("/chats/" + cid); c != 200 || !strings.Contains(h, "پیام اول هماهنگی") {
			t.Fatal("archive lost", c)
		}
	})
	t.Run("sampling remains active until result, cancellation archives immediately", func(t *testing.T) {
		var id string
		a.DB.QueryRow(ctx, "SELECT booking_id FROM consultation_chats WHERE id=$1", sampleCID).Scan(&id)
		exec("UPDATE bookings SET status='delivered' WHERE id=$1", id)
		send(sampler, sampleCID, "هماهنگی نتیجه", 200)
		exec("UPDATE bookings SET status='result_ready' WHERE id=$1", id)
		send(owner, sampleCID, "پس از نتیجه", 409)
		id, c2 := fixture("grooming")
		exec("UPDATE bookings SET status='cancel_requested',refund_status='pending_refund' WHERE id=$1", id)
		send(owner, c2, "پس از لغو", 409)
	})
}
