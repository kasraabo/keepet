package app

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	assets "keepet"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func uploadOnboardingDoc(t *testing.T, b *browser, id, label string, data []byte, want int) {
	t.Helper()
	var body bytes.Buffer
	m := multipart.NewWriter(&body)
	m.WriteField("csrf", b.csrf)
	m.WriteField("document_type", label)
	f, _ := m.CreateFormFile("receipt", "certificate.pdf")
	f.Write(data)
	m.Close()
	req, _ := http.NewRequest("POST", b.base+"/applications/"+id+"/documents", &body)
	req.Header.Set("Content-Type", m.FormDataContentType())
	res, e := b.client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != want {
		t.Fatalf("document %d want %d: %s", res.StatusCode, want, raw)
	}
}

type onboardingTransport func(*http.Request) (*http.Response, error)

func (f onboardingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestOnboarding(t *testing.T) {
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
	t.Setenv("GOOGLE_CLIENT_ID", "phase7-test-client")
	t.Setenv("GOOGLE_CLIENT_SECRET", "phase7-test-secret")
	t.Setenv("GOOGLE_REDIRECT_URL", "https://keepet.example/auth/google/callback")
	t.Setenv("SESSION_COOKIE_SECURE", "")
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
	exec("TRUNCATE users CASCADE;TRUNCATE business_config")
	if e = a.seed(ctx); e != nil {
		t.Fatal(e)
	}
	// Exercise the actual data-upgrade SQL against a legacy non-vet form, retaining custom questions.
	exec(`UPDATE business_config SET data=jsonb_set(data,'{questions}',data->'questions'||'[ {"id":"license","label":"مجوز قدیمی","type":"text"}, {"id":"custom_keep","label":"سؤال سفارشی","type":"text"} ]'::jsonb) WHERE kind='form' AND id='signup-grooming'`)
	migrationSQL, err := assets.Files.ReadFile("migrations/000018_onboarding.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	dataSQL := strings.Split(strings.Split(string(migrationSQL), "-- Remove the old generic license question only from non-veterinary forms.")[1], "ALTER TABLE applications")[0]
	exec(dataSQL)
	kept := false
	for _, q := range a.form(ctx, "signup-grooming", nil).Questions {
		if q.ID == "license" {
			t.Fatal("legacy non-vet license survived migration")
		}
		if q.ID == "custom_keep" {
			kept = true
		}
	}
	if !kept {
		t.Fatal("migration discarded custom questions")
	}
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	post := func(b *browser, path string, v url.Values, want int) string {
		t.Helper()
		h, c := b.post(path, v)
		if c != want {
			t.Fatalf("%s: %d want %d: %s", path, c, want, h)
		}
		return h
	}
	otp := func(b *browser, phone, next, pending string) string {
		t.Helper()
		h := post(b, "/auth/request", url.Values{"phone": {phone}, "next": {next}, "google_attempt": {pending}}, 200)
		if field(h, "name") != "" || strings.Contains(h, `name="name"`) {
			t.Fatal("name asked during OTP")
		}
		return h
	}
	verify := func(b *browser, h string, extra url.Values, want int) string {
		t.Helper()
		code := regexp.MustCompile(`data-dev-code>([0-9]{6})`).FindStringSubmatch(h)
		if len(code) != 2 {
			t.Fatalf("missing OTP: %s", h)
		}
		v := url.Values{"challenge": {field(h, "challenge")}, "code": {code[1]}, "terms": {"on"}}
		for k, values := range extra {
			v[k] = values
		}
		out := post(b, "/auth/verify", v, want)
		if want == 200 && field(out, "csrf") != "" {
			b.csrf = field(out, "csrf")
		}
		return out
	}
	t.Run("role routes and durable name", func(t *testing.T) {
		b := newBrowser(t, srv)
		h, c := b.get("/provider/login")
		if c != 200 || !strings.Contains(h, "ورود و ثبت‌نام متخصصان") {
			t.Fatal("provider login")
		}
		h = otp(b, "09129991001", "/dashboard", "")
		h = verify(b, h, url.Values{"name": {"نام قبلی"}}, 200)
		if !strings.Contains(h, `value="نام قبلی"`) {
			t.Fatal("name not prefilled in profile")
		}
		post(b, "/profile", url.Values{"full_name": {"نام کامل ذخیره شده"}, "next": {"/dashboard"}}, 200)
		exec("UPDATE otp_challenges SET created_at=now()-interval '2 minutes' WHERE phone='09129991001'")
		other := newBrowser(t, srv)
		h = otp(other, "09129991001", "/dashboard", "")
		if strings.Contains(h, `name="terms"`) {
			t.Fatal("terms asked again")
		}
		verify(other, h, url.Values{"name": {"نام جعلی"}, "next": {"/management"}}, 200)
		var name string
		a.DB.QueryRow(ctx, "SELECT name FROM users WHERE phone='09129991001'").Scan(&name)
		if name != "نام کامل ذخیره شده" {
			t.Fatal("returning name overwritten")
		}
	})
	t.Run("guest draft survives OTP and another account is not reassigned", func(t *testing.T) {
		b := newBrowser(t, srv)
		var st string
		for _, c := range b.client.Jar.Cookies(mustParseOnboardingURL(t, srv.URL)) {
			if c.Name == "keepet_session" {
				st = c.Value
			}
		}
		bid := token()
		exec("INSERT INTO bookings(id,session_token,service,payment_key,pet_name,age,stage,answers) VALUES($1,$2,'vet',$3,'پت محفوظ','۲ سال',2,'{\"symptom\":\"مقدار محفوظ\"}')", bid, st, token())
		owned := token()
		exec("INSERT INTO bookings(id,session_token,owner_id,service,payment_key,pet_name) VALUES($1,$2,'demo-vet','vet',$3,'پت حساب دیگر')", owned, st, token())
		h := otp(b, "09129991002", "/booking/"+bid, "")
		verify(b, h, url.Values{"next": {"https://evil.example"}}, 200)
		var preserved, notStolen bool
		a.DB.QueryRow(ctx, "SELECT b.pet_name='پت محفوظ' AND b.stage=2 AND b.answers->>'symptom'='مقدار محفوظ' AND u.phone='09129991002' AND b.session_token<>$2 FROM bookings b JOIN users u ON u.id=b.owner_id WHERE b.id=$1", bid, st).Scan(&preserved)
		a.DB.QueryRow(ctx, "SELECT owner_id='demo-vet' FROM bookings WHERE id=$1", owned).Scan(&notStolen)
		if !preserved || !notStolen {
			t.Fatal("draft lost or existing ownership reassigned")
		}
	})
	t.Run("specialist OTP and private documents", func(t *testing.T) {
		b := newBrowser(t, srv)
		h := otp(b, "09129991003", "/join", "")
		verify(b, h, nil, 200)
		h = post(b, "/join", url.Values{"service": {"vet"}, "name": {"پزشک جدید"}, "license": {"test-license"}, "bio": {"توضیحات حرفه‌ای"}, "price": {"300000"}}, 200)
		if !strings.Contains(h, "هنوز مدرکی بارگذاری نشده") {
			t.Fatal("document redirect")
		}
		var id, doc string
		a.DB.QueryRow(ctx, "SELECT id FROM applications WHERE name='پزشک جدید'").Scan(&id)
		uploadOnboardingDoc(t, b, id, "کارت نظام دامپزشکی", []byte("invalid"), 400)
		uploadOnboardingDoc(t, b, id, "کارت نظام دامپزشکی", []byte("%PDF-1.4\ncertificate\n%%EOF"), 200)
		a.DB.QueryRow(ctx, "SELECT id FROM application_documents WHERE application_id=$1", id).Scan(&doc)
		stranger := newBrowser(t, srv)
		stranger.login("09120000003")
		if _, c := stranger.get("/applications/" + id + "/documents/" + doc); c != 403 {
			t.Fatal("private document leak")
		}
		res, e := b.client.Get(srv.URL + "/applications/" + id + "/documents/" + doc)
		if e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
		if res.StatusCode != 200 || res.Header.Get("Cache-Control") != "private, no-store" || !strings.Contains(res.Header.Get("Content-Disposition"), "attachment") {
			t.Fatal("download protection")
		}
		ops := newBrowser(t, srv)
		ops.login("09120000001")
		post(ops, "/ops/applications/"+id, url.Values{"decision": {"approved"}, "verified": {"on"}}, 200)
		uploadOnboardingDoc(t, b, id, "مدرک تحصیلی", []byte("%PDF-1.4\nnew\n%%EOF"), 409)
		for _, scope := range []string{"signup-grooming", "signup-sampling"} {
			for _, q := range a.form(ctx, scope, nil).Questions {
				if q.ID == "license" {
					t.Fatal("veterinary license requested for non-vet")
				}
			}
		}
	})
	// An isolated transport signs ID tokens locally; no tests contact Google or use real credentials.
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	encode := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	sign := func(c googleClaims) string {
		h := encode(map[string]string{"alg": "RS256", "kid": "test-key"})
		b := encode(c)
		sum := sha256.Sum256([]byte(h + "." + b))
		sig, e := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
		if e != nil {
			t.Fatal(e)
		}
		return h + "." + b + "." + base64.RawURLEncoding.EncodeToString(sig)
	}
	identity := "google-test-subject"
	var exchangeVerifier string
	a.GoogleHTTP = &http.Client{Transport: onboardingTransport(func(r *http.Request) (*http.Response, error) {
		var payload any
		switch r.URL.String() {
		case "https://oauth2.googleapis.com/token":
			r.ParseForm()
			exchangeVerifier = r.Form.Get("code_verifier")
			var nonce string
			a.DB.QueryRow(ctx, "SELECT nonce FROM google_login_attempts WHERE verifier=$1", exchangeVerifier).Scan(&nonce)
			payload = map[string]string{"id_token": sign(googleClaims{Issuer: "https://accounts.google.com", Subject: identity, Audience: os.Getenv("GOOGLE_CLIENT_ID"), Email: "verified@example.test", Verified: true, Name: "نام گوگل", Nonce: nonce, Expiry: time.Now().Add(time.Hour).Unix(), Issued: time.Now().Unix()})}
		case "https://www.googleapis.com/oauth2/v3/certs":
			payload = map[string]any{"keys": []map[string]string{{"kid": "test-key", "kty": "RSA", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}}
		default:
			t.Fatalf("unexpected network request %s", r.URL)
		}
		b, _ := json.Marshal(payload)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b)), Header: http.Header{}}, nil
	})}
	start := func(b *browser, next string) *url.URL {
		t.Helper()
		v := url.Values{"csrf": {b.csrf}, "next": {next}}
		req, _ := http.NewRequest("POST", srv.URL+"/auth/google", strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		res, e := b.client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		to, e := url.Parse(res.Header.Get("HX-Redirect"))
		if e != nil || to.Host != "accounts.google.com" {
			t.Fatal("Google authorization redirect")
		}
		return to
	}
	callback := func(b *browser, state string, want int) string {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+"/auth/google/callback?state="+url.QueryEscape(state)+"&code=test-code", nil)
		req.Header.Set("HX-Request", "true")
		res, e := b.client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("callback %d want %d: %s", res.StatusCode, want, raw)
		}
		if h := res.Header.Get("HX-Redirect"); h != "" {
			return h
		}
		return string(raw)
	}
	t.Run("Google proof nonce audience signature", func(t *testing.T) {
		good := googleClaims{Issuer: "accounts.google.com", Subject: "subject", Audience: os.Getenv("GOOGLE_CLIENT_ID"), Email: "ok@example.test", Verified: true, Nonce: "nonce", Expiry: time.Now().Add(time.Hour).Unix(), Issued: time.Now().Unix()}
		if _, e := a.verifyGoogleToken(ctx, sign(good), "nonce"); e != nil {
			t.Fatal(e)
		}
		for _, mutate := range []func(*googleClaims){func(c *googleClaims) { c.Audience = "other" }, func(c *googleClaims) { c.Verified = false }, func(c *googleClaims) { c.Expiry = 1 }, func(c *googleClaims) { c.Nonce = "wrong" }, func(c *googleClaims) { c.Issuer = "evil" }} {
			c := good
			mutate(&c)
			if _, e := a.verifyGoogleToken(ctx, sign(c), "nonce"); e == nil {
				t.Fatal("bad claims accepted")
			}
		}
		raw := sign(good)
		parts := strings.Split(raw, ".")
		parts[2] = base64.RawURLEncoding.EncodeToString(make([]byte, 256))
		if _, e := a.verifyGoogleToken(ctx, strings.Join(parts, "."), "nonce"); e == nil {
			t.Fatal("bad signature accepted")
		}
	})
	t.Run("Google requires phone link then permits return login", func(t *testing.T) {
		b := newBrowser(t, srv)
		to := start(b, "/dashboard")
		state := to.Query().Get("state")
		other := newBrowser(t, srv)
		callback(other, state, 400)
		h := callback(b, state, 200)
		pending := field(h, "google_attempt")
		if pending == "" || exchangeVerifier == "" {
			t.Fatal("missing verified pending link / PKCE")
		}
		sum := sha256.Sum256([]byte(exchangeVerifier))
		if to.Query().Get("code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) {
			t.Fatal("PKCE mismatch")
		}
		callback(b, state, 400)
		// The pending identity is browser-bound even after successful OAuth.
		post(other, "/auth/request", url.Values{"phone": {"09129991010"}, "google_attempt": {pending}}, 409)
		h = otp(b, "09129991010", "/management", pending)
		h = verify(b, h, nil, 200)
		if !strings.Contains(h, `value="نام گوگل"`) {
			t.Fatal("Google name not preserved")
		}
		var role string
		a.DB.QueryRow(ctx, "SELECT u.role FROM google_identities g JOIN users u ON u.id=g.user_id WHERE subject=$1", identity).Scan(&role)
		if role != "owner" {
			t.Fatal("Google granted privilege")
		}
		fresh := newBrowser(t, srv)
		to = start(fresh, "/dashboard")
		if got := callback(fresh, to.Query().Get("state"), 200); got != "/dashboard" {
			t.Fatal("return login", got)
		}
		identity = "conflicting-subject"
		conflict := newBrowser(t, srv)
		to = start(conflict, "/dashboard")
		h = callback(conflict, to.Query().Get("state"), 200)
		pending = field(h, "google_attempt")
		exec("UPDATE otp_challenges SET created_at=now()-interval '2 minutes' WHERE phone='09129991010'")
		h = otp(conflict, "09129991010", "/dashboard", pending)
		verify(conflict, h, nil, 409)
		identity = "another-subject"
		attack := newBrowser(t, srv)
		to = start(attack, "/dashboard")
		h = callback(attack, to.Query().Get("state"), 200)
		pending = field(h, "google_attempt")
		h = otp(attack, "09120000005", "/dashboard", pending)
		verify(attack, h, nil, 409)
		var n int
		a.DB.QueryRow(ctx, "SELECT count(*) FROM google_identities WHERE user_id='demo-admin'").Scan(&n)
		if n != 0 {
			t.Fatal("admin Google binding")
		}
	})
}
func mustParseOnboardingURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, e := url.Parse(s)
	if e != nil {
		t.Fatal(e)
	}
	return u
}
