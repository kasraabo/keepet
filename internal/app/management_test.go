package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagement(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL test database")
	}
	t.Setenv("APP_MODE", "development")
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("ADMIN_SETTINGS_KEY", "")
	t.Setenv("SETTINGS_KEY_FILE", filepath.Join(t.TempDir(), "settings.key"))
	ctx := context.Background()
	a, e := New(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	var db string
	a.DB.QueryRow(ctx, "SELECT current_database()").Scan(&db)
	if !strings.HasSuffix(db, "_test") {
		t.Fatal("refusing non-test database")
	}
	if _, e = a.DB.Exec(ctx, "TRUNCATE users CASCADE; TRUNCATE business_config"); e != nil {
		t.Fatal(e)
	}
	if e = a.seed(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = a.DB.Exec(ctx, `UPDATE platform_integrations SET settings='{"digits":"6","ttl":"120","resend":"30","hourly":"5","attempts":"5"}',secret=NULL,revision=1 WHERE id='otp'`); e != nil {
		t.Fatal(e)
	}
	defer func() {
		a.DB.Exec(ctx, `UPDATE platform_integrations SET settings='{"digits":"6","ttl":"120","resend":"30","hourly":"5","attempts":"5"}',secret=NULL,revision=1 WHERE id='otp'`)
	}()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	admin := newBrowser(t, srv)
	admin.login("09120000005")
	owner := newBrowser(t, srv)
	owner.login("09129999999")
	save := func(id string, v url.Values, want int) {
		t.Helper()
		var rev int64
		a.DB.QueryRow(ctx, "SELECT revision FROM platform_integrations WHERE id=$1", id).Scan(&rev)
		v.Set("revision", fmtInt(rev))
		h, c := admin.post("/management/integrations/"+id, v)
		if c != want {
			t.Fatalf("integration %s status %d body %s", id, c, h)
		}
	}
	t.Run("separate chrome login routes and access", func(t *testing.T) {
		guest := newBrowser(t, srv)
		h, c := guest.get("/management")
		if c != 200 || !strings.Contains(h, "ورود مدیر") || strings.Contains(h, "join-banner") || strings.Contains(h, "/manifest.webmanifest") {
			t.Fatal("not a separate admin login")
		}
		for _, p := range []string{"/management", "/management/reports", "/management/integrations", "/management/operations", "/management/catalog", "/management/content", "/management/triage", "/management/reports.csv"} {
			h, c := admin.get(p)
			if c != 200 || p != "/management/reports.csv" && !strings.Contains(h, "management-body") {
				t.Fatalf("route %s %d", p, c)
			}
			if strings.Contains(h, "hero-assurance") || strings.Contains(h, "footer-links") {
				t.Fatal("storefront chrome leaked")
			}
			if _, c := owner.get(p); c != 403 {
				t.Fatalf("owner allowed %s", p)
			}
		}
	})
	t.Run("operational writes stay in management and deny owners", func(t *testing.T) {
		day := time.Now().In(a.Location).AddDate(0, 0, 8).Format("2006-01-02")
		v := url.Values{"provider": {"vet-nazanin"}, "day": {day}, "period": {"noon"}, "capacity": {"3"}}
		h, c := admin.post("/management/operations/availability", v)
		if c != 200 || !strings.Contains(h, "management-body") || strings.Contains(h, "class=\"header\"") {
			t.Fatal("operation escaped standalone management")
		}
		v.Set("csrf", owner.csrf)
		if _, c = owner.post("/management/operations/availability", v); c != 403 {
			t.Fatal("owner operational write allowed")
		}
	})
	t.Run("encrypted credentials redaction keep replace clear and revisions", func(t *testing.T) {
		secret := "11111111-2222-4333-8444-555555555555"
		v := url.Values{"mode": {"sandbox"}, "callback_url": {"https://keepet.example/payments/callback"}, "description": {"خدمات کیپت"}, "secret": {secret}}
		save("zarinpal", v, 200)
		var raw []byte
		a.DB.QueryRow(ctx, "SELECT secret FROM platform_integrations WHERE id='zarinpal'").Scan(&raw)
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("plaintext credentials")
		}
		opened, e := openIntegration("zarinpal", raw)
		if e != nil || opened != secret {
			t.Fatal("encryption roundtrip")
		}
		if _, e = openIntegration("sms", raw); e == nil {
			t.Fatal("ciphertext moved between providers")
		}
		key, e := os.Stat(os.Getenv("SETTINGS_KEY_FILE"))
		if e != nil || key.Mode().Perm() != 0600 {
			t.Fatal("insecure key file")
		}
		h, _ := admin.get("/management/integrations")
		if strings.Contains(h, secret) {
			t.Fatal("HTML credential leak")
		}
		h, _ = admin.get("/management/audit")
		if strings.Contains(h, secret) {
			t.Fatal("audit credential leak")
		}
		v.Del("secret")
		save("zarinpal", v, 200)
		var retained []byte
		a.DB.QueryRow(ctx, "SELECT secret FROM platform_integrations WHERE id='zarinpal'").Scan(&retained)
		if !bytes.Equal(raw, retained) {
			t.Fatal("blank secret erased existing value")
		}
		v.Set("revision", "1")
		if _, c := admin.post("/management/integrations/zarinpal", v); c != 409 {
			t.Fatal("stale config accepted")
		}
		v.Set("clear_secret", "on")
		save("zarinpal", v, 200)
		var absent bool
		a.DB.QueryRow(ctx, "SELECT secret IS NULL FROM platform_integrations WHERE id='zarinpal'").Scan(&absent)
		if !absent {
			t.Fatal("secret not cleared")
		}
		v.Del("clear_secret")
		v.Set("callback_url", "http://evil.example")
		save("zarinpal", v, 400)
		if _, c := owner.post("/management/integrations/zarinpal", v); c != 403 {
			t.Fatal("owner mutation allowed")
		}
	})
	t.Run("snappay email and SMS configuration", func(t *testing.T) {
		save("snappay", url.Values{"mode": {"sandbox"}, "merchant_id": {"demo"}, "api_base": {"https://merchant.example/api"}, "secret": {`{"client_id":"demo","client_secret":"snapp-test-secret"}`}}, 200)
		save("snappay", url.Values{"mode": {"sandbox"}, "secret": {"not json"}}, 400)
		save("email", url.Values{"host": {"smtp.example"}, "port": {"587"}, "username": {"test"}, "from": {"hello@keepet.example"}, "secret": {"smtp-test-secret"}}, 200)
		save("email", url.Values{"host": {"smtp.example"}, "port": {"25"}}, 400)
		save("sms", url.Values{"sender": {"1000"}, "template": {"keepet-otp"}, "secret": {"sms-test-secret"}}, 200)
		h, _ := admin.get("/management/integrations")
		for _, secret := range []string{"snapp-test-secret", "smtp-test-secret", "sms-test-secret"} {
			if strings.Contains(h, secret) {
				t.Fatal("secret displayed")
			}
		}
	})
	t.Run("OTP policy affects new challenges and snapshots older challenges", func(t *testing.T) {
		guest := newBrowser(t, srv)
		h, c := guest.post("/auth/request", url.Values{"phone": {"09129999998"}, "next": {"/dashboard"}})
		if c != 200 {
			t.Fatal("old challenge request")
		}
		oldID := field(h, "challenge")
		save("otp", url.Values{"digits": {"4"}, "ttl": {"60"}, "resend": {"30"}, "hourly": {"5"}, "attempts": {"3"}}, 200)
		var attempts, digits int
		a.DB.QueryRow(ctx, "SELECT max_attempts,code_digits FROM otp_challenges WHERE id=$1", oldID).Scan(&attempts, &digits)
		if attempts != 5 || digits != 6 {
			t.Fatal("existing OTP policy altered")
		}
		h, c = guest.post("/auth/request", url.Values{"phone": {"09129999997"}, "next": {"/dashboard"}})
		if c != 200 {
			t.Fatal("new challenge request")
		}
		id := field(h, "challenge")
		var expiry time.Time
		a.DB.QueryRow(ctx, "SELECT max_attempts,code_digits,expires_at FROM otp_challenges WHERE id=$1", id).Scan(&attempts, &digits, &expiry)
		if attempts != 3 || digits != 4 || time.Until(expiry) > 61*time.Second {
			t.Fatal("OTP settings not applied")
		}
		if !strings.Contains(h, `maxlength="4"`) {
			t.Fatal("OTP input length wrong")
		}
		save("otp", url.Values{"digits": {"2"}, "ttl": {"60"}, "resend": {"1"}, "hourly": {"5"}, "attempts": {"3"}}, 400)
	})
	t.Run("exact KPI definitions filtered days and no fake growth", func(t *testing.T) {
		a.DB.Exec(ctx, "DELETE FROM bookings")
		var session string
		a.DB.QueryRow(ctx, "SELECT token FROM sessions WHERE user_id='demo-admin' LIMIT 1").Scan(&session)
		now := time.Now().In(a.Location)
		day := now.AddDate(0, 0, -5).Format("2006-01-02")
		at, _ := time.ParseInLocation("2006-01-02 15:04", day+" 12:00", a.Location)
		for i, st := range []string{"completed", "paid", "cancelled"} {
			amount := int64((i + 1) * 100000)
			refund := int64(0)
			rs := ""
			if st == "cancelled" {
				refund = amount
				rs = "refunded"
			}
			_, e = a.DB.Exec(ctx, `INSERT INTO bookings(id,session_token,owner_id,service,status,payment_status,amount,commission_bps,payment_key,paid_at,created_at,refund_amount,refund_status) VALUES($1,$2,'demo-admin','vet',$3,'paid',$4,2000,$5,$6,$6,$7,$8)`, token(), session, st, amount, token(), at, refund, rs)
			if e != nil {
				t.Fatal(e)
			}
		}
		_, e = a.DB.Exec(ctx, `INSERT INTO bookings(id,session_token,service,status,payment_key,created_at) VALUES($1,$2,'vet','draft',$3,$4)`, token(), session, token(), at)
		if e != nil {
			t.Fatal(e)
		}
		req := httptest.NewRequest("GET", "/management?from="+day+"&to="+day, nil)
		v, e := a.loadReport(ctx, req)
		if e != nil {
			t.Fatal(e)
		}
		if v.Paid != 3 || v.Gross != 600000 || v.Refund != 300000 || v.Fees != 20000 || v.Completed != 1 || v.Cancelled != 1 || v.Customers != 1 || v.Repeat != 1 || len(v.Trend) != 1 || v.Trend[0].Amount != 600000 {
			raw, _ := json.Marshal(v)
			t.Fatalf("wrong KPI %s", raw)
		}
		if v.Cards[3].Value != money(200000) || v.Cards[5].Value != "—" || v.Cards[8].Value != rate(3, 4) {
			t.Fatal("wrong AOV growth or conversion")
		}
		for _, u := range []string{"/management?from=bad", "/management?from=2000-01-01&to=2025-01-01", "/management?from=2099-01-01&to=2099-01-02"} {
			if _, c := admin.get(u); c != 400 {
				t.Fatal("invalid report range accepted")
			}
		}
		h, c := admin.get("/management/reports.csv?from=" + day + "&to=" + day)
		if c != 200 || !strings.Contains(h, "GMV") {
			t.Fatal("KPI CSV missing")
		}
	})
}
