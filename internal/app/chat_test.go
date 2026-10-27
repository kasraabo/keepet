package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// chatRequest deliberately uses the real HTTP middleware and the caller's cookie
// jar: media and multipart authorization must work independently of the UI.
func chatRequest(t *testing.T, b *browser, method, path, contentType string, body io.Reader) (string, int, http.Header) {
	t.Helper()
	r, err := http.NewRequest(method, b.base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Accept", "application/json")
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	res, err := b.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), res.StatusCode, res.Header.Clone()
}

func chatMultipart(t *testing.T, b *browser, chatID, csrf, text, filename, mime string, data []byte) (string, int, http.Header) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("csrf", csrf); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("text", text); err != nil {
		t.Fatal(err)
	}
	if filename != "" {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
		h.Set("Content-Type", mime)
		part, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return chatRequest(t, b, http.MethodPost, "/chats/"+chatID+"/messages", mw.FormDataContentType(), &body)
}

func chatPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	img.SetNRGBA(1, 1, color.NRGBA{R: 40, G: 150, B: 100, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func chatWAV() []byte {
	// 64 ms of mono 16-bit PCM at 8 kHz, with a genuine RIFF/WAVE header.
	pcm := make([]byte, 1024)
	b := make([]byte, 44+len(pcm))
	copy(b[0:4], "RIFF")
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)-8))
	copy(b[8:12], "WAVE")
	copy(b[12:16], "fmt ")
	binary.LittleEndian.PutUint32(b[16:20], 16)
	binary.LittleEndian.PutUint16(b[20:22], 1)
	binary.LittleEndian.PutUint16(b[22:24], 1)
	binary.LittleEndian.PutUint32(b[24:28], 8000)
	binary.LittleEndian.PutUint32(b[28:32], 16000)
	binary.LittleEndian.PutUint16(b[32:34], 2)
	binary.LittleEndian.PutUint16(b[34:36], 16)
	copy(b[36:40], "data")
	binary.LittleEndian.PutUint32(b[40:44], uint32(len(pcm)))
	copy(b[44:], pcm)
	return b
}

func TestConsultationChat(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL test database")
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
	var dbname string
	if err = a.DB.QueryRow(ctx, "SELECT current_database()").Scan(&dbname); err != nil || !strings.HasSuffix(dbname, "_test") {
		t.Fatal("refusing to reset a database not suffixed _test")
	}
	if _, err = a.DB.Exec(ctx, "TRUNCATE users CASCADE; TRUNCATE business_config CASCADE"); err != nil {
		t.Fatal(err)
	}
	if err = a.seed(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(ctx, `UPDATE platform_integrations SET settings='{"digits":"6","ttl":"120","resend":"30","hourly":"5","attempts":"5"}',secret=NULL,revision=1 WHERE id='otp'`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = a.DB.Exec(ctx, "TRUNCATE business_config CASCADE")
		_ = a.seedBusiness(ctx)
	}()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	owner := newBrowser(t, srv)
	owner.login("09128881111")
	other := newBrowser(t, srv)
	other.login("09128881112")
	vet := newBrowser(t, srv)
	vet.login("09120000002")
	vet2 := newBrowser(t, srv)
	vet2.login("09120000006")
	groomer := newBrowser(t, srv)
	groomer.login("09120000003")
	admin := newBrowser(t, srv)
	admin.login("09120000005")
	ops := newBrowser(t, srv)
	ops.login("09120000001")
	var ownerID, ownerSession string
	if err = a.DB.QueryRow(ctx, "SELECT id FROM users WHERE phone='09128881111'").Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	cookieURL, _ := url.Parse(srv.URL)
	for _, c := range owner.client.Jar.Cookies(cookieURL) {
		if c.Name == "keepet_session" {
			ownerSession = c.Value
		}
	}
	if ownerSession == "" {
		t.Fatal("missing owner session")
	}
	fixture := func(st string) string {
		t.Helper()
		id := token()
		_, e := a.DB.Exec(ctx, `INSERT INTO bookings(id,session_token,owner_id,service,stage,pet_name,species,provider_id,amount,payment_key,status,payment_status,paid_at,exact_time) VALUES($1,$2,$3,'vet',4,'پت چت','cat','vet-nazanin',350000,$4,$5,'paid',now(),now())`, id, ownerSession, ownerID, token(), st)
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	start := func() (string, string) {
		t.Helper()
		bid := fixture("accepted")
		body, code := vet.post("/provider/bookings/"+bid, url.Values{"action": {"start"}})
		if code != 200 {
			t.Fatalf("start chat %d: %s", code, body)
		}
		var id string
		if e := a.DB.QueryRow(ctx, "SELECT id FROM consultation_chats WHERE booking_id=$1", bid).Scan(&id); e != nil {
			t.Fatal(e)
		}
		return bid, id
	}
	postText := func(b *browser, cid, text string, want int) string {
		t.Helper()
		values := url.Values{"csrf": {b.csrf}, "text": {text}}
		body, code, headers := chatRequest(t, b, http.MethodPost, "/chats/"+cid+"/messages", "application/x-www-form-urlencoded", strings.NewReader(values.Encode()))
		if code != want {
			t.Fatalf("message status %d want %d: %s", code, want, body)
		}
		if code == 200 {
			var v map[string]any
			if err := json.Unmarshal([]byte(body), &v); err != nil || v["ok"] != true || !strings.Contains(headers.Get("Content-Type"), "application/json") {
				t.Fatalf("invalid message JSON: %s", body)
			}
		}
		return body
	}
	clinical := func(cid, feedback string) (string, int) {
		t.Helper()
		v := url.Values{"symptoms": {"بی‌اشتهایی و بی‌حالی"}, "diagnosis": {"تشخیص افتراقی؛ نیاز به معاینه"}, "prescription": {"مراجعه حضوری؛ <script>clinical-xss</script>"}}
		if feedback != "" {
			v.Set("customer_rating", "4")
			v.Set("customer_feedback", feedback)
		}
		return vet.post("/chats/"+cid+"/questionnaire", v)
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if e := a.DB.QueryRow(ctx, query, args...).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}

	t.Run("full consultation preserves transcript and separate required forms", func(t *testing.T) {
		bid, cid := start()
		var minutes int
		var startedAt, expiresAt time.Time
		if e := a.DB.QueryRow(ctx, "SELECT duration_minutes,started_at,expires_at FROM consultation_chats WHERE id=$1", cid).Scan(&minutes, &startedAt, &expiresAt); e != nil {
			t.Fatal(e)
		}
		if minutes != 30 || expiresAt.Sub(startedAt) != 30*time.Minute {
			t.Fatalf("wrong initial consultation lifetime: %d %s", minutes, expiresAt.Sub(startedAt))
		}
		text := "سلام، حال پت من خوب نیست 🐈 <script>chat-xss</script>"
		postText(owner, cid, text, 200)
		postText(vet, cid, "شرح حال را دریافت کردم.", 200)
		if body, code := owner.get("/chats/" + cid); code != 200 || strings.Contains(body, "<script>chat-xss</script>") {
			t.Fatalf("unsafe transcript HTML %d: %s", code, body)
		}
		messages, code, _ := chatRequest(t, vet, http.MethodGet, "/chats/"+cid+"/messages", "", nil)
		if code != 200 || !strings.Contains(messages, "سلام") || !json.Valid([]byte(messages)) {
			t.Fatalf("missing transcript JSON %d %s", code, messages)
		}
		if _, code := owner.post("/chats/"+cid+"/end", nil); code != 403 {
			t.Fatalf("customer ended clinical consultation: %d", code)
		}
		if _, code := vet.post("/chats/"+cid+"/questionnaire", url.Values{"symptoms": {"شرح علائم"}, "diagnosis": {""}, "prescription": {""}}); code != 400 {
			t.Fatalf("incomplete clinical form status %d", code)
		}
		if count("SELECT count(*) FROM consultation_vet_forms WHERE chat_id=$1", cid) != 0 {
			t.Fatal("incomplete clinical form persisted")
		}
		if body, code := vet.post("/chats/"+cid+"/end", nil); code != 200 {
			t.Fatalf("end %d: %s", code, body)
		}
		postText(owner, cid, "ارسال پس از پایان", 409)
		if count("SELECT count(*) FROM consultation_chats WHERE id=$1 AND closed_at IS NOT NULL", cid) != 1 {
			t.Fatal("room not archived after ending")
		}
		if body, code := clinical(cid, "نظر محرمانه دامپزشک درباره مشتری private-vet-feedback"); code != 200 {
			t.Fatalf("clinical questionnaire %d %s", code, body)
		}
		var st, prescription string
		if e := a.DB.QueryRow(ctx, "SELECT status,prescription FROM bookings WHERE id=$1", bid).Scan(&st, &prescription); e != nil {
			t.Fatal(e)
		}
		if st != "completed" || !strings.Contains(prescription, "مراجعه حضوری") || strings.Contains(prescription, "private-vet-feedback") {
			t.Fatalf("invalid clinical completion %s %s", st, prescription)
		}
		if count("SELECT count(*) FROM consultation_customer_forms WHERE chat_id=$1", cid) != 0 {
			t.Fatal("completion fabricated customer feedback")
		}
		if body, code := owner.post("/chats/"+cid+"/questionnaire", url.Values{"rating": {"5"}, "communication": {"4"}}); code != 400 {
			t.Fatalf("incomplete customer questionnaire accepted %d %s", code, body)
		}
		if body, code := owner.post("/chats/"+cid+"/questionnaire", url.Values{"rating": {"5"}, "communication": {"4"}, "clarity": {"5"}, "body": {"پاسخ‌ها روشن بود"}}); code != 200 {
			t.Fatalf("customer questionnaire %d %s", code, body)
		}
		if count("SELECT count(*) FROM consultation_customer_forms WHERE chat_id=$1", cid) != 1 {
			t.Fatal("customer questionnaire missing")
		}
		for _, path := range []string{"/chats/" + cid, "/chats/" + cid + "/messages", "/bookings/" + bid, "/chats"} {
			body, code := owner.get(path)
			if code != 200 || strings.Contains(body, "private-vet-feedback") || strings.Contains(body, "<script>clinical-xss</script>") {
				t.Fatalf("customer private data/HTML leak on %s %d: %s", path, code, body)
			}
		}
		if count("SELECT count(*) FROM booking_events WHERE booking_id=$1 AND detail LIKE '%private-vet-feedback%'", bid) != 0 || count("SELECT count(*) FROM outbox WHERE booking_id=$1 AND payload::text LIKE '%private-vet-feedback%'", bid) != 0 {
			t.Fatal("private veterinarian opinion leaked into customer events")
		}
		before := count("SELECT count(*) FROM booking_events WHERE booking_id=$1 AND event='completed'", bid)
		if before != 1 {
			t.Fatalf("completion event count %d", before)
		}
		// A replay may be idempotent or explicitly rejected; neither may create
		// duplicate clinical records or repeat a completion notification.
		_, code = clinical(cid, "نظر محرمانه دامپزشک درباره مشتری private-vet-feedback")
		if code != 200 && code != 409 {
			t.Fatalf("clinical replay %d", code)
		}
		if count("SELECT count(*) FROM consultation_vet_forms WHERE chat_id=$1", cid) != 1 || count("SELECT count(*) FROM booking_events WHERE booking_id=$1 AND event='completed'", bid) != before {
			t.Fatal("clinical replay duplicated completion")
		}
		for _, viewer := range []*browser{owner, vet, admin, ops} {
			if body, code := viewer.get("/chats/" + cid); code != 200 {
				t.Fatalf("authorized archive unavailable %d %s", code, body)
			}
		}
		if count("SELECT count(*) FROM consultation_messages WHERE chat_id=$1", cid) != 2 {
			t.Fatal("archival changed the transcript")
		}
	})

	t.Run("unrelated identities denied and care team read only", func(t *testing.T) {
		_, cid := start()
		postText(owner, cid, "privacy-sentinel-clinical-chat", 200)
		for _, intruder := range []*browser{other, vet2, groomer} {
			for _, path := range []string{"/chats/" + cid, "/chats/" + cid + "/messages"} {
				body, code := intruder.get(path)
				if code != 404 || strings.Contains(body, "privacy-sentinel-clinical-chat") {
					t.Fatalf("unrelated consultation read %d %s", code, body)
				}
			}
			postText(intruder, cid, "unauthorized", 404)
			if _, code := intruder.post("/chats/"+cid+"/questionnaire", url.Values{"symptoms": {"fake"}, "diagnosis": {"fake"}, "prescription": {"fake"}}); code != 404 {
				t.Fatalf("unrelated questionnaire %d", code)
			}
			body, code := intruder.get("/chats")
			if code != 200 || strings.Contains(body, cid) || strings.Contains(body, "privacy-sentinel-clinical-chat") {
				t.Fatalf("unrelated consultation listed %d %s", code, body)
			}
		}
		for _, viewer := range []*browser{admin, ops} {
			if _, code := viewer.get("/chats/" + cid); code != 200 {
				t.Fatalf("authorized care team read %d", code)
			}
			postText(viewer, cid, "care team cannot impersonate participant", 403)
			if _, code := viewer.post("/chats/"+cid+"/questionnaire", url.Values{"symptoms": {"fake"}, "diagnosis": {"fake"}, "prescription": {"fake"}}); code != 403 {
				t.Fatalf("care team submitted participant form %d", code)
			}
		}
		if count("SELECT count(*) FROM consultation_messages WHERE chat_id=$1", cid) != 1 {
			t.Fatal("unauthorized messages persisted")
		}
	})

	t.Run("real image and audio uploads stay private", func(t *testing.T) {
		_, cid := start()
		pngData := chatPNG(t)
		wavData := chatWAV()
		for _, file := range []struct {
			name, mime string
			data       []byte
		}{{"pet.png", "image/png", pngData}, {"symptoms.wav", "audio/wav", wavData}} {
			body, code, _ := chatMultipart(t, owner, cid, owner.csrf, "شرح فایل", file.name, file.mime, file.data)
			if code != 200 || !json.Valid([]byte(body)) {
				t.Fatalf("valid upload %s %d %s", file.name, code, body)
			}
			var attachmentID string
			if e := a.DB.QueryRow(ctx, `SELECT a.id FROM consultation_attachments a JOIN consultation_messages m ON m.id=a.message_id WHERE m.chat_id=$1 ORDER BY m.id DESC LIMIT 1`, cid).Scan(&attachmentID); e != nil {
				t.Fatal(e)
			}
			for _, reader := range []*browser{owner, vet, admin, ops} {
				data, code, h := chatRequest(t, reader, http.MethodGet, "/chat-media/"+attachmentID, "", nil)
				if code != 200 || !bytes.Equal([]byte(data), file.data) || !strings.HasPrefix(h.Get("Content-Type"), strings.Split(file.mime, "/")[0]+"/") || !strings.Contains(h.Get("Cache-Control"), "no-store") || h.Get("X-Content-Type-Options") != "nosniff" {
					t.Fatalf("unsafe/private media %s status=%d content-type=%q cache=%q", file.name, code, h.Get("Content-Type"), h.Get("Cache-Control"))
				}
			}
			for _, intruder := range []*browser{other, vet2, groomer} {
				data, code, _ := chatRequest(t, intruder, http.MethodGet, "/chat-media/"+attachmentID, "", nil)
				if code != 404 || bytes.Equal([]byte(data), file.data) {
					t.Fatalf("unrelated user received media %d", code)
				}
			}
		}
		if count(`SELECT count(*) FROM consultation_attachments a JOIN consultation_messages m ON m.id=a.message_id WHERE m.chat_id=$1`, cid) != 2 {
			t.Fatal("valid attachments not persisted")
		}
	})

	t.Run("upload size MIME CSRF and text validation prevent persistence", func(t *testing.T) {
		_, cid := start()
		cases := []struct {
			name, mime string
			data       []byte
		}{
			{"unsafe.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)},
			{"fake.png", "image/png", []byte("<html><script>alert(1)</script></html>")},
			{"fake.wav", "audio/wav", []byte("not a valid audio recording")},
			{"broken.png", "image/png", []byte{137, 'P', 'N', 'G', '\r', '\n', 26, '\n'}},
		}
		for _, file := range cases {
			body, code, _ := chatMultipart(t, owner, cid, owner.csrf, "", file.name, file.mime, file.data)
			if code != 400 {
				t.Fatalf("invalid MIME accepted %s %d %s", file.name, code, body)
			}
		}
		if _, code, _ := chatMultipart(t, owner, cid, "invalid-csrf", "", "pet.png", "image/png", chatPNG(t)); code != 403 {
			t.Fatalf("multipart CSRF status %d", code)
		}
		oversize := append(chatPNG(t), make([]byte, 8<<20)...)
		if body, code, _ := chatMultipart(t, owner, cid, owner.csrf, "", "too-large.png", "image/png", oversize); code != 400 && code != 413 {
			t.Fatalf("oversize image accepted %d %s", code, body)
		}
		postText(owner, cid, "", 400)
		postText(owner, cid, strings.Repeat("ب", 4001), 400)
		if count("SELECT count(*) FROM consultation_messages WHERE chat_id=$1", cid) != 0 || count(`SELECT count(*) FROM consultation_attachments a JOIN consultation_messages m ON m.id=a.message_id WHERE m.chat_id=$1`, cid) != 0 {
			t.Fatal("invalid upload/text left persisted messages or files")
		}
		// The limit is characters rather than bytes: a valid Persian message
		// must not lose half of its allowed length to UTF-8 encoding.
		postText(owner, cid, strings.Repeat("ب", 4000), 200)
	})

	t.Run("expiry is authoritative and archival preserves unanswered forms", func(t *testing.T) {
		bid, cid := start()
		postText(owner, cid, "message retained after expiry", 200)
		if _, e := a.DB.Exec(ctx, "UPDATE consultation_chats SET expires_at=now()-interval '1 minute' WHERE id=$1", cid); e != nil {
			t.Fatal(e)
		}
		postText(vet, cid, "server rejects expired sends", 409)
		if body, code := owner.get("/chats/" + cid); code != 200 {
			t.Fatalf("expired archive unavailable %d %s", code, body)
		}
		if count("SELECT count(*) FROM consultation_chats WHERE id=$1 AND closed_at IS NOT NULL", cid) != 1 {
			t.Fatal("expired room not archived")
		}
		if count("SELECT count(*) FROM consultation_vet_forms WHERE chat_id=$1", cid) != 0 || count("SELECT count(*) FROM consultation_customer_forms WHERE chat_id=$1", cid) != 0 {
			t.Fatal("expiry fabricated questionnaire responses")
		}
		if body, code := clinical(cid, ""); code != 200 {
			t.Fatalf("clinical form after expiry %d %s", code, body)
		}
		var st string
		if e := a.DB.QueryRow(ctx, "SELECT status FROM bookings WHERE id=$1", bid).Scan(&st); e != nil || st != "completed" {
			t.Fatalf("clinical report after expiry did not complete booking %s %v", st, e)
		}
	})

	t.Run("cancellation archives room and prevents later completion", func(t *testing.T) {
		bid, cid := start()
		postText(vet, cid, "discussion before platform cancellation", 200)
		if body, code := ops.post("/ops/bookings/"+bid, url.Values{"action": {"platform_cancel"}, "notes": {"عدم تحقق خدمت و بررسی جبران"}}); code != 200 {
			t.Fatalf("platform cancellation %d %s", code, body)
		}
		postText(owner, cid, "late send", 409)
		if body, code := owner.get("/chats/" + cid); code != 200 {
			t.Fatalf("cancelled consultation archive missing %d %s", code, body)
		}
		body, code := clinical(cid, "")
		if code != 200 && code != 409 {
			t.Fatalf("cancelled clinical form %d %s", code, body)
		}
		var st string
		if e := a.DB.QueryRow(ctx, "SELECT status FROM bookings WHERE id=$1", bid).Scan(&st); e != nil || st != "cancel_requested" {
			t.Fatalf("clinical submission resurrected cancelled booking %s %v", st, e)
		}
	})

	t.Run("vet cannot complete clinical visit through old prescription shortcut", func(t *testing.T) {
		bid, cid := start()
		if body, code := vet.post("/provider/bookings/"+bid, url.Values{"action": {"complete"}, "prescription": {"legacy clinical shortcut"}}); code != 409 {
			t.Fatalf("clinical questionnaire bypass %d %s", code, body)
		}
		if body, code := clinical(cid, ""); code != 200 {
			t.Fatalf("optional customer opinion wrongly required %d %s", code, body)
		}
		var st string
		if e := a.DB.QueryRow(ctx, "SELECT status FROM bookings WHERE id=$1", bid).Scan(&st); e != nil || st != "in_progress" {
			t.Fatalf("clinical questionnaire closed active discussion %s %v", st, e)
		}
		if body, code := vet.post("/chats/"+cid+"/end", nil); code != 200 {
			t.Fatalf("end after clinical form %d %s", code, body)
		}
		if e := a.DB.QueryRow(ctx, "SELECT status FROM bookings WHERE id=$1", bid).Scan(&st); e != nil || st != "completed" {
			t.Fatalf("end failed to complete submitted clinical visit %s %v", st, e)
		}
	})

	t.Run("rate limit counts sender separately", func(t *testing.T) {
		_, cid := start()
		for i := 0; i < 20; i++ {
			postText(owner, cid, "bounded message sending", 200)
		}
		postText(owner, cid, "sender rate limited", 429)
		postText(vet, cid, "other participant can still reply", 200)
		if count("SELECT count(*) FROM consultation_messages WHERE chat_id=$1", cid) != 21 {
			t.Fatal("rate limiting persisted excess messages or blocked veterinarian")
		}
	})

	t.Run("staff can change an active visit duration without stale overwrites or reopening", func(t *testing.T) {
		_, cid := start()
		var started, originalExpiry time.Time
		if e := a.DB.QueryRow(ctx, "SELECT started_at,expires_at FROM consultation_chats WHERE id=$1", cid).Scan(&started, &originalExpiry); e != nil {
			t.Fatal(e)
		}
		change := func(b *browser, minutes, expected string) (string, int) {
			return b.post("/management/chats/"+cid+"/duration", url.Values{"duration_minutes": {minutes}, "expected_expires": {expected}})
		}
		for _, caller := range []*browser{owner, other, vet, groomer} {
			if body, code := change(caller, "60", originalExpiry.Format(time.RFC3339)); code != 403 {
				t.Fatalf("participant changed visit duration %d %s", code, body)
			}
		}
		for _, invalid := range []string{"4", "241", "invalid"} {
			if body, code := change(admin, invalid, originalExpiry.Format(time.RFC3339)); code != 400 {
				t.Fatalf("invalid per-visit duration %q %d %s", invalid, code, body)
			}
		}
		if _, code := admin.post("/management/chats/"+cid+"/duration", url.Values{"csrf": {"wrong"}, "duration_minutes": {"60"}, "expected_expires": {originalExpiry.Format(time.RFC3339)}}); code != 403 {
			t.Fatalf("duration CSRF not enforced: %d", code)
		}
		if body, code := change(admin, "۶۰", originalExpiry.Format(time.RFC3339)); code != 200 {
			t.Fatalf("valid per-visit duration %d %s", code, body)
		}
		var expiry time.Time
		var minutes int
		if e := a.DB.QueryRow(ctx, "SELECT expires_at,duration_minutes FROM consultation_chats WHERE id=$1", cid).Scan(&expiry, &minutes); e != nil {
			t.Fatal(e)
		}
		if minutes != 60 || !expiry.Equal(started.Add(time.Hour)) {
			t.Fatalf("duration is not anchored to original start: %d %s", minutes, expiry)
		}
		if _, code := change(admin, "90", originalExpiry.Format(time.RFC3339)); code != 409 {
			t.Fatalf("stale per-visit duration accepted: %d", code)
		}
		if body, code := change(ops, "45", expiry.Format(time.RFC3339)); code != 200 {
			t.Fatalf("authorized operations duration change %d %s", code, body)
		}
		if count("SELECT count(*) FROM business_audit WHERE kind='chat_duration' AND target=$1", cid) != 2 {
			t.Fatal("invalid/stale duration mutation left an audit or valid mutation was not audited")
		}
		if e := a.DB.QueryRow(ctx, "SELECT expires_at FROM consultation_chats WHERE id=$1", cid).Scan(&expiry); e != nil {
			t.Fatal(e)
		}
		if body, code := admin.post("/chats/"+cid+"/end", nil); code != 200 {
			t.Fatalf("staff ending room %d %s", code, body)
		}
		if _, code := change(admin, "240", expiry.Format(time.RFC3339)); code != 409 {
			t.Fatalf("staff reopened an ended room: %d", code)
		}
		if count("SELECT count(*) FROM consultation_chats WHERE id=$1 AND closed_at IS NOT NULL AND duration_minutes=45", cid) != 1 {
			t.Fatal("closed room duration/state changed")
		}
		_, oldVisit := start()
		if _, e := a.DB.Exec(ctx, "UPDATE consultation_chats SET started_at=now()-interval '10 minutes',expires_at=now()+interval '20 minutes' WHERE id=$1", oldVisit); e != nil {
			t.Fatal(e)
		}
		if e := a.DB.QueryRow(ctx, "SELECT expires_at FROM consultation_chats WHERE id=$1", oldVisit).Scan(&expiry); e != nil {
			t.Fatal(e)
		}
		if body, code := admin.post("/management/chats/"+oldVisit+"/duration", url.Values{"duration_minutes": {"5"}, "expected_expires": {expiry.Format(time.RFC3339)}}); code != 400 {
			t.Fatalf("duration shortened into the past %d %s", code, body)
		}
		var unchanged time.Time
		if e := a.DB.QueryRow(ctx, "SELECT expires_at FROM consultation_chats WHERE id=$1", oldVisit).Scan(&unchanged); e != nil || !unchanged.Equal(expiry) {
			t.Fatalf("invalid shortening changed expiry %s %v", unchanged, e)
		}
	})

	t.Run("start chat upgrades legacy visits and preserves existing room identity and lifetime", func(t *testing.T) {
		bid := fixture("in_progress")
		if count("SELECT count(*) FROM consultation_chats WHERE booking_id=$1", bid) != 0 {
			t.Fatal("legacy fixture unexpectedly has a room")
		}
		if body, code := vet.post("/provider/bookings/"+bid, url.Values{"action": {"start_chat"}}); code != 200 {
			t.Fatalf("legacy chat upgrade %d %s", code, body)
		}
		var cid, originalOwner, originalVet string
		var originalStart, originalExpiry time.Time
		if e := a.DB.QueryRow(ctx, "SELECT id,owner_id,vet_id,started_at,expires_at FROM consultation_chats WHERE booking_id=$1", bid).Scan(&cid, &originalOwner, &originalVet, &originalStart, &originalExpiry); e != nil {
			t.Fatal(e)
		}
		if originalOwner != ownerID || originalVet != "demo-vet" {
			t.Fatal("legacy visit created the wrong participants")
		}
		if _, code := vet2.post("/provider/bookings/"+bid, url.Values{"action": {"start_chat"}}); code != 404 {
			t.Fatalf("unassigned veterinarian upgraded a visit: %d", code)
		}
		for i := 0; i < 2; i++ {
			if body, code := vet.post("/provider/bookings/"+bid, url.Values{"action": {"start_chat"}, "owner_id": {"demo-admin"}, "provider": {"vet-arman"}}); code != 200 {
				t.Fatalf("repeated chat start %d %s", code, body)
			}
		}
		var nowID, nowOwner, nowVet string
		var nowStart, nowExpiry time.Time
		if e := a.DB.QueryRow(ctx, "SELECT id,owner_id,vet_id,started_at,expires_at FROM consultation_chats WHERE booking_id=$1", bid).Scan(&nowID, &nowOwner, &nowVet, &nowStart, &nowExpiry); e != nil {
			t.Fatal(e)
		}
		if nowID != cid || nowOwner != originalOwner || nowVet != originalVet || !nowStart.Equal(originalStart) || !nowExpiry.Equal(originalExpiry) || count("SELECT count(*) FROM consultation_chats WHERE booking_id=$1", bid) != 1 {
			t.Fatal("repeated start reset or reassigned existing consultation")
		}
		if _, e := a.DB.Exec(ctx, "UPDATE consultation_chats SET expires_at=now()-interval '1 minute' WHERE id=$1", cid); e != nil {
			t.Fatal(e)
		}
		if e := a.DB.QueryRow(ctx, "SELECT expires_at FROM consultation_chats WHERE id=$1", cid).Scan(&originalExpiry); e != nil {
			t.Fatal(e)
		}
		body, code := vet.post("/provider/bookings/"+bid, url.Values{"action": {"start_chat"}})
		if code != 200 && code != 409 {
			t.Fatalf("expired room repeated start %d %s", code, body)
		}
		if e := a.DB.QueryRow(ctx, "SELECT expires_at FROM consultation_chats WHERE id=$1", cid).Scan(&nowExpiry); e != nil || !nowExpiry.Equal(originalExpiry) {
			t.Fatalf("repeated start extended an expired room %s %v", nowExpiry, e)
		}
		postText(owner, cid, "expired visit cannot be restarted by its veterinarian", 409)
		if count("SELECT count(*) FROM consultation_chats WHERE id=$1 AND closed_at IS NOT NULL", cid) != 1 {
			t.Fatal("repeated expired start reopened consultation")
		}
		paid := fixture("paid")
		if _, code := vet.post("/provider/bookings/"+paid, url.Values{"action": {"start_chat"}}); code != 409 || count("SELECT count(*) FROM consultation_chats WHERE booking_id=$1", paid) != 0 {
			t.Fatalf("unaccepted visit upgrade status %d", code)
		}
	})

	t.Run("maintenance archives without visits and only completes submitted clinical forms once", func(t *testing.T) {
		withoutForm, withoutChat := start()
		withForm, withChat := start()
		if body, code := clinical(withChat, "private-maintenance-feedback"); code != 200 {
			t.Fatalf("clinical form before maintenance %d %s", code, body)
		}
		if _, e := a.DB.Exec(ctx, "UPDATE consultation_chats SET started_at=now()-interval '31 minutes',expires_at=now()-interval '1 minute' WHERE id=ANY($1::text[])", []string{withoutChat, withChat}); e != nil {
			t.Fatal(e)
		}
		// No request reads either chat after the timestamp update. The worker
		// helper alone must persist closure and clinical completion.
		if e := a.reconcileChats(ctx); e != nil {
			t.Fatal(e)
		}
		for _, cid := range []string{withoutChat, withChat} {
			if count("SELECT count(*) FROM consultation_chats WHERE id=$1 AND close_reason='expired' AND closed_at=expires_at", cid) != 1 {
				t.Fatal("maintenance did not persist expiry at the scheduled cutoff")
			}
		}
		var pending, complete, prescription string
		if e := a.DB.QueryRow(ctx, "SELECT status FROM bookings WHERE id=$1", withoutForm).Scan(&pending); e != nil {
			t.Fatal(e)
		}
		if e := a.DB.QueryRow(ctx, "SELECT status,prescription FROM bookings WHERE id=$1", withForm).Scan(&complete, &prescription); e != nil {
			t.Fatal(e)
		}
		if pending != "in_progress" || complete != "completed" || !strings.Contains(prescription, "مراجعه حضوری") || strings.Contains(prescription, "private-maintenance-feedback") {
			t.Fatalf("maintenance completion ignored clinical boundary: pending=%s complete=%s prescription=%s", pending, complete, prescription)
		}
		if count("SELECT count(*) FROM consultation_vet_forms WHERE chat_id=$1", withoutChat) != 0 || count("SELECT count(*) FROM consultation_customer_forms WHERE chat_id=ANY($1::text[])", []string{withoutChat, withChat}) != 0 {
			t.Fatal("maintenance invented missing participant forms")
		}
		for i := 0; i < 2; i++ {
			if e := a.reconcileChats(ctx); e != nil {
				t.Fatal(e)
			}
		}
		if count("SELECT count(*) FROM booking_events WHERE booking_id=$1 AND event='completed'", withForm) != 1 || count("SELECT count(*) FROM outbox WHERE booking_id=$1 AND kind='completed'", withForm) != 1 {
			t.Fatal("maintenance repeated clinical completion notifications")
		}
		if body, code := clinical(withoutChat, ""); code != 200 {
			t.Fatalf("pending form after worker archival %d %s", code, body)
		}
		if e := a.reconcileChats(ctx); e != nil {
			t.Fatal(e)
		}
		if count("SELECT count(*) FROM booking_events WHERE booking_id=$1 AND event='completed'", withoutForm) != 1 {
			t.Fatal("later clinical form did not complete exactly once")
		}
	})

	t.Run("concurrent staff ending and participant forms preserve actor isolation", func(t *testing.T) {
		bid, cid := start()
		type actorRequest struct {
			client *http.Client
			req    *http.Request
		}
		var requests []actorRequest
		for _, actor := range []struct {
			browser *browser
			path    string
			values  url.Values
		}{
			{vet, "/chats/" + cid + "/questionnaire", url.Values{"symptoms": {"real veterinarian symptoms"}, "diagnosis": {"real veterinarian diagnosis"}, "prescription": {"real veterinarian prescription"}, "customer_feedback": {"private-concurrent-vet-opinion"}}},
			{admin, "/chats/" + cid + "/end", url.Values{}},
			{owner, "/chats/" + cid + "/questionnaire", url.Values{"rating": {"5"}, "communication": {"4"}, "clarity": {"3"}, "body": {"private-concurrent-customer-survey"}, "symptoms": {"forged customer symptoms"}, "diagnosis": {"forged customer diagnosis"}, "prescription": {"forged customer prescription"}}},
		} {
			actor.values.Set("csrf", actor.browser.csrf)
			r, e := http.NewRequest(http.MethodPost, actor.browser.base+actor.path, strings.NewReader(actor.values.Encode()))
			if e != nil {
				t.Fatal(e)
			}
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			requests = append(requests, actorRequest{actor.browser.client, r})
		}
		type result struct {
			code int
			err  error
			body string
		}
		gate := make(chan struct{})
		results := make(chan result, len(requests))
		for _, actor := range requests {
			go func(actor actorRequest) {
				<-gate
				res, e := actor.client.Do(actor.req)
				if e != nil {
					results <- result{err: e}
					return
				}
				defer res.Body.Close()
				body, e := io.ReadAll(res.Body)
				results <- result{code: res.StatusCode, err: e, body: string(body)}
			}(actor)
		}
		close(gate)
		for range requests {
			r := <-results
			if r.err != nil || r.code != 200 {
				t.Fatalf("concurrent participant action failed: %d %v %s", r.code, r.err, r.body)
			}
		}
		var st, symptoms, diagnosis, prescription string
		if e := a.DB.QueryRow(ctx, `SELECT b.status,f.symptoms,f.diagnosis,f.prescription FROM bookings b JOIN consultation_chats c ON c.booking_id=b.id JOIN consultation_vet_forms f ON f.chat_id=c.id WHERE b.id=$1`, bid).Scan(&st, &symptoms, &diagnosis, &prescription); e != nil {
			t.Fatal(e)
		}
		if st != "completed" || symptoms != "real veterinarian symptoms" || diagnosis != "real veterinarian diagnosis" || prescription != "real veterinarian prescription" {
			t.Fatalf("concurrent actor crossed clinical ownership: %s %s %s %s", st, symptoms, diagnosis, prescription)
		}
		if count("SELECT count(*) FROM consultation_customer_forms WHERE chat_id=$1 AND rating=5 AND communication=4 AND clarity=3", cid) != 1 || count("SELECT count(*) FROM booking_events WHERE booking_id=$1 AND event='completed'", bid) != 1 || count("SELECT count(*) FROM outbox WHERE booking_id=$1 AND kind='completed'", bid) != 1 {
			t.Fatal("concurrent clinical completion/survey was lost or duplicated")
		}
		if body, code := owner.get("/chats/" + cid); code != 200 || strings.Contains(body, "private-concurrent-vet-opinion") {
			t.Fatalf("concurrent veterinary opinion leak %d %s", code, body)
		}
		if body, code := vet.get("/chats/" + cid); code != 200 || strings.Contains(body, "private-concurrent-customer-survey") {
			t.Fatalf("concurrent customer survey leak %d %s", code, body)
		}
	})

	t.Run("duration is configurable audited and snapshotted", func(t *testing.T) {
		_, oldChat := start()
		var revision int64
		if e := a.DB.QueryRow(ctx, "SELECT revision FROM business_config WHERE kind='chat' AND id='consultation'").Scan(&revision); e != nil {
			t.Fatal(e)
		}
		values := url.Values{"revision": {fmtInt(revision)}, "duration_minutes": {"۶۰"}}
		if body, code := owner.post("/management/chat/consultation", values); code != 403 {
			t.Fatalf("customer updated chat duration %d %s", code, body)
		}
		values.Del("csrf") // post adds the caller's CSRF; do not reuse the owner's token.
		if body, code := admin.post("/management/chat/consultation", values); code != 200 {
			t.Fatalf("change duration %d %s", code, body)
		}
		if _, code := admin.post("/management/chat/consultation", values); code != 409 {
			t.Fatalf("stale chat duration form status %d", code)
		}
		if e := a.DB.QueryRow(ctx, "SELECT revision FROM business_config WHERE kind='chat' AND id='consultation'").Scan(&revision); e != nil {
			t.Fatal(e)
		}
		for _, invalid := range []string{"0", "4", "241", "invalid"} {
			if body, code := admin.post("/management/chat/consultation", url.Values{"revision": {fmtInt(revision)}, "duration_minutes": {invalid}}); code != 400 {
				t.Fatalf("invalid duration %q %d %s", invalid, code, body)
			}
		}
		_, newChat := start()
		var oldMinutes, newMinutes int
		if e := a.DB.QueryRow(ctx, "SELECT duration_minutes FROM consultation_chats WHERE id=$1", oldChat).Scan(&oldMinutes); e != nil {
			t.Fatal(e)
		}
		if e := a.DB.QueryRow(ctx, "SELECT duration_minutes FROM consultation_chats WHERE id=$1", newChat).Scan(&newMinutes); e != nil {
			t.Fatal(e)
		}
		if oldMinutes != 30 || newMinutes != 60 {
			t.Fatalf("duration changed existing record or did not update new visit: %d %d", oldMinutes, newMinutes)
		}
		if count("SELECT count(*) FROM business_audit WHERE kind='chat' AND target='consultation'") != 1 {
			t.Fatal("chat duration changes were not audited exactly once")
		}
		if e := a.seedBusiness(ctx); e != nil {
			t.Fatal(e)
		}
		var persisted int
		if e := a.DB.QueryRow(ctx, "SELECT (data->>'duration_minutes')::integer FROM business_config WHERE kind='chat' AND id='consultation'").Scan(&persisted); e != nil || persisted != 60 {
			t.Fatalf("restart seeding reset consultation configuration %d %v", persisted, e)
		}
	})
}
