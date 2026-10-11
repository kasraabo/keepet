package app

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type IntegrationField struct {
	Key, Label, Value, Type string
	Required                bool
}
type IntegrationView struct {
	ID, Name, Description, SecretLabel, Status string
	Revision                                   int64
	HasSecret                                  bool
	Fields                                     []IntegrationField
}
type integrationDefinition struct {
	Name, Description, SecretLabel string
	Fields                         []IntegrationField
}

var integrationDefinitions = map[string]integrationDefinition{
	"zarinpal": {"زرین‌پال", "تنظیم شناسهٔ پذیرنده و آدرس بازگشت. پرداخت‌های فعلی همچنان آزمایشی‌اند؛ ذخیرهٔ مشخصات به معنی اتصال یا تأیید درگاه نیست.", "Merchant ID", []IntegrationField{{Key: "mode", Label: "محیط sandbox یا live", Type: "text", Required: true}, {Key: "callback_url", Label: "آدرس HTTPS بازگشت", Type: "url"}, {Key: "description", Label: "شرح پرداخت", Type: "text"}}},
	"snappay":  {"اسنپ‌پی · پرداخت اقساطی", "اطلاعات پذیرندگی و اعتبارنامهٔ API ذخیره می‌شود. اتصال و نمایش پرداخت اقساطی پس از دریافت قرارداد فنی پذیرنده پیاده‌سازی خواهد شد؛ هنوز فعال نیست.", "اعتبارنامهٔ API (JSON طبق مستندات پذیرنده)", []IntegrationField{{Key: "mode", Label: "محیط sandbox یا live", Type: "text", Required: true}, {Key: "merchant_id", Label: "شناسهٔ پذیرنده", Type: "text"}, {Key: "api_base", Label: "آدرس HTTPS API طبق مستندات پذیرنده", Type: "url"}, {Key: "callback_url", Label: "آدرس HTTPS بازگشت", Type: "url"}}},
	"sms":      {"OTP پیامکی · کاوه‌نگار", "شمارهٔ فرستنده، نام الگوی تأیید و کلید API. ارسال واقعی در این نسخه فعال نیست؛ کد توسعه روی صفحه نمایش داده می‌شود.", "کلید API", []IntegrationField{{Key: "sender", Label: "شمارهٔ فرستنده", Type: "text"}, {Key: "template", Label: "نام الگوی Verify Lookup", Type: "text"}}},
	"email":    {"OTP ایمیلی · SMTP", "تنظیم سرور ارسال ایمیل با TLS، حساب فرستنده و رمز. ورود ایمیلی و ارسال واقعی هنوز فعال نیستند؛ مشخصات برای اتصال نگهداری می‌شوند.", "رمز یا App Password", []IntegrationField{{Key: "host", Label: "نام سرور SMTP", Type: "text"}, {Key: "port", Label: "پورت SMTP (۴۶۵ یا ۵۸۷)", Type: "number"}, {Key: "username", Label: "نام کاربری SMTP", Type: "text"}, {Key: "from", Label: "ایمیل فرستنده", Type: "email"}}},
	"otp":      {"قواعد کد تأیید", "این قواعد روی کدهای ورود جدید سایت و پنل مدیریت اعمال می‌شوند. اعتبار و تعداد تلاش هر کد هنگام صدور ثبت می‌شود.", "", []IntegrationField{{Key: "digits", Label: "تعداد رقم (۴ تا ۶)", Type: "number", Required: true}, {Key: "ttl", Label: "اعتبار به ثانیه (۶۰ تا ۶۰۰)", Type: "number", Required: true}, {Key: "resend", Label: "فاصلهٔ ارسال مجدد به ثانیه (۳۰ تا ۱۲۰)", Type: "number", Required: true}, {Key: "hourly", Label: "سقف کد هر شماره در ساعت (۱ تا ۱۰)", Type: "number", Required: true}, {Key: "attempts", Label: "تلاش مجاز هر کد (۳ تا ۵)", Type: "number", Required: true}}},
}

func (a *App) integrationViews(ctx context.Context) ([]IntegrationView, error) {
	var out []IntegrationView
	for _, id := range []string{"zarinpal", "snappay", "sms", "email", "otp"} {
		d := integrationDefinitions[id]
		v := IntegrationView{ID: id, Name: d.Name, Description: d.Description, SecretLabel: d.SecretLabel}
		var raw []byte
		if e := a.DB.QueryRow(ctx, "SELECT settings,secret IS NOT NULL,revision FROM platform_integrations WHERE id=$1", id).Scan(&raw, &v.HasSecret, &v.Revision); e != nil {
			return nil, e
		}
		settings := map[string]string{}
		if e := json.Unmarshal(raw, &settings); e != nil {
			return nil, e
		}
		for _, f := range d.Fields {
			f.Value = settings[f.Key]
			v.Fields = append(v.Fields, f)
		}
		v.Status = "نیازمند تنظیم"
		if v.HasSecret {
			v.Status = "اعتبارنامه ذخیره شده · اتصال بررسی نشده"
		}
		if id == "otp" {
			v.Status = "فعال برای کدهای جدید"
		}
		out = append(out, v)
	}
	return out, nil
}

// The encryption key lives outside PostgreSQL and is never included in HTML, audit or source control.
func settingsCipher() (cipher.AEAD, error) {
	var key []byte
	var e error
	if env := os.Getenv("ADMIN_SETTINGS_KEY"); env != "" {
		key, e = base64.StdEncoding.DecodeString(env)
	} else {
		path := os.Getenv("SETTINGS_KEY_FILE")
		if path == "" {
			return nil, fmt.Errorf("SETTINGS_KEY_FILE or ADMIN_SETTINGS_KEY is required")
		}
		key, e = os.ReadFile(path)
		if os.IsNotExist(e) {
			if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
				return nil, e
			}
			key = make([]byte, 32)
			if _, e = rand.Read(key); e != nil {
				return nil, e
			}
			var f *os.File
			f, e = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if os.IsExist(e) {
				key, e = os.ReadFile(path)
			} else if e == nil {
				_, e = f.Write(key)
				closeErr := f.Close()
				if e == nil {
					e = closeErr
				}
			}
		}
	}
	if e != nil || len(key) != 32 {
		return nil, fmt.Errorf("encryption key unavailable or invalid")
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(block)
}
func sealIntegration(id, value string) ([]byte, error) {
	c, e := settingsCipher()
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, c.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	return c.Seal(nonce, nonce, []byte(value), []byte(id)), nil
}
func openIntegration(id string, raw []byte) (string, error) {
	c, e := settingsCipher()
	if e != nil {
		return "", e
	}
	if len(raw) < c.NonceSize() {
		return "", fmt.Errorf("invalid ciphertext")
	}
	p, e := c.Open(nil, raw[:c.NonceSize()], raw[c.NonceSize():], []byte(id))
	return string(p), e
}
func validateIntegration(id string, s map[string]string, secret string) error {
	for _, v := range s {
		if len(v) > 500 || strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("مقدار تنظیم نامعتبر است")
		}
	}
	for _, key := range []string{"api_base", "callback_url"} {
		if v := s[key]; v != "" {
			u, e := url.Parse(v)
			if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
				return fmt.Errorf("آدرس باید HTTPS و بدون رمز یا fragment باشد")
			}
		}
	}
	if id == "zarinpal" || id == "snappay" {
		if !contains([]string{"sandbox", "live"}, s["mode"]) {
			return fmt.Errorf("محیط باید sandbox یا live باشد")
		}
	}
	if id == "zarinpal" && secret != "" {
		if !regexpMerchant.MatchString(secret) {
			return fmt.Errorf("Merchant ID باید UUID معتبر باشد")
		}
	}
	if id == "snappay" && secret != "" {
		var obj map[string]string
		if json.Unmarshal([]byte(secret), &obj) != nil || len(obj) == 0 {
			return fmt.Errorf("اعتبارنامهٔ اسنپ‌پی باید JSON از نام و مقدارهای متنی باشد")
		}
	}
	if id == "email" {
		if s["port"] != "465" && s["port"] != "587" {
			return fmt.Errorf("پورت TLS باید ۴۶۵ یا ۵۸۷ باشد")
		}
		if s["from"] != "" {
			if _, e := mail.ParseAddress(s["from"]); e != nil {
				return fmt.Errorf("ایمیل فرستنده معتبر نیست")
			}
		}
		if strings.ContainsAny(s["host"], " /\\:@") {
			return fmt.Errorf("نام سرور SMTP معتبر نیست")
		}
	}
	if id == "otp" {
		ranges := map[string][2]int{"digits": {4, 6}, "ttl": {60, 600}, "resend": {30, 120}, "hourly": {1, 10}, "attempts": {3, 5}}
		for k, r := range ranges {
			n, e := strconv.Atoi(s[k])
			if e != nil || n < r[0] || n > r[1] {
				return fmt.Errorf("مقدار %s خارج از محدوده است", k)
			}
		}
	}
	return nil
}
func (a *App) saveIntegration(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	id := r.PathValue("id")
	d, ok := integrationDefinitions[id]
	if !ok {
		a.fail(w, r, "سرویس پیدا نشد", 404)
		return
	}
	settings := map[string]string{}
	for _, f := range d.Fields {
		settings[f.Key] = strings.TrimSpace(r.FormValue(f.Key))
	}
	secret := strings.TrimSpace(r.FormValue("secret"))
	if secret != "" && r.FormValue("clear_secret") == "on" {
		a.fail(w, r, "حذف اعتبارنامه و واردکردن مقدار تازه را همزمان انتخاب نکنید", 400)
		return
	}
	if len(secret) > 16000 {
		a.fail(w, r, "اعتبارنامه بیش از حد طولانی است", 400)
		return
	}
	if e := validateIntegration(id, settings, secret); e != nil {
		a.fail(w, r, e.Error(), 400)
		return
	}
	var sealed []byte
	var e error
	if secret != "" {
		sealed, e = sealIntegration(id, secret)
		if e != nil {
			a.fail(w, r, "کلید رمزگذاری در دسترس نیست؛ تنظیم مسیر SETTINGS_KEY_FILE را بررسی کنید", 503)
			return
		}
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	var before, oldSecret []byte
	var rev int64
	e = tx.QueryRow(ctx, "SELECT settings,secret,revision FROM platform_integrations WHERE id=$1 FOR UPDATE", id).Scan(&before, &oldSecret, &rev)
	expected, parseErr := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	if e != nil || parseErr != nil || expected != rev {
		a.fail(w, r, "فرم قدیمی است؛ صفحه را تازه کنید", 409)
		return
	}
	if secret == "" {
		sealed = oldSecret
	}
	if r.FormValue("clear_secret") == "on" {
		sealed = nil
	}
	after, _ := json.Marshal(settings)
	_, e = tx.Exec(ctx, "UPDATE platform_integrations SET settings=$2,secret=$3,revision=revision+1,updated_by=$4,updated_at=now() WHERE id=$1", id, after, sealed, sess(r).User.ID)
	// Audit records public configuration only. Never copy ciphertext or plaintext credentials.
	if e == nil {
		audit, _ := json.Marshal(map[string]any{"settings": settings, "credential_present": len(sealed) > 0, "credential_changed": secret != "" || r.FormValue("clear_secret") == "on"})
		_, e = tx.Exec(ctx, "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,'integration',$2,$3,$4)", sess(r).User.ID, id, before, audit)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "ذخیرهٔ تنظیمات انجام نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/management/integrations", "تنظیمات ذخیره شد. فعال‌سازی اتصال واقعی مستقل از ذخیرهٔ اطلاعات است."))
}

type OTPPolicy struct{ Digits, TTL, Resend, Hourly, Attempts int }

func (a *App) otpPolicy(ctx context.Context) OTPPolicy {
	p := OTPPolicy{6, 120, 30, 5, 5}
	var raw []byte
	if a.DB.QueryRow(ctx, "SELECT settings FROM platform_integrations WHERE id='otp'").Scan(&raw) != nil {
		return p
	}
	var s map[string]string
	if json.Unmarshal(raw, &s) != nil || validateIntegration("otp", s, "") != nil {
		return p
	}
	p.Digits, _ = strconv.Atoi(s["digits"])
	p.TTL, _ = strconv.Atoi(s["ttl"])
	p.Resend, _ = strconv.Atoi(s["resend"])
	p.Hourly, _ = strconv.Atoi(s["hourly"])
	p.Attempts, _ = strconv.Atoi(s["attempts"])
	return p
}
func (a *App) managementLogin(w http.ResponseWriter, r *http.Request) {
	if sess(r).User != nil && sess(r).User.Role == "admin" {
		redirect(w, r, "/management")
		return
	}
	p := a.page(r, "ورود مدیر پلتفرم")
	p.Next = "/management"
	a.render(w, r, "management-login", p)
}
func (a *App) renderAuth(w http.ResponseWriter, r *http.Request, name string, p Page) {
	if strings.HasPrefix(p.Next, "/management") {
		name = "management-" + name
	}
	a.render(w, r, name, p)
}
func (a *App) managementLogout(w http.ResponseWriter, r *http.Request) {
	a.DB.Exec(r.Context(), "UPDATE sessions SET expires_at=now() WHERE token=$1", sess(r).Token)
	http.SetCookie(w, &http.Cookie{Name: "keepet_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	redirect(w, r, "/management/login")
}

var regexpMerchant = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (a *App) seedIntegrations(ctx context.Context) error {
	_, e := a.DB.Exec(ctx, `INSERT INTO platform_integrations(id,settings) VALUES
 ('zarinpal','{"mode":"sandbox","callback_url":"","description":"پرداخت خدمات کیپت"}'),
 ('snappay','{"mode":"sandbox","merchant_id":"","api_base":"","callback_url":""}'),
 ('sms','{"provider":"kavenegar","sender":"","template":"","mode":"development"}'),
 ('email','{"host":"","port":"587","username":"","from":"","mode":"development"}'),
 ('otp','{"digits":"6","ttl":"120","resend":"30","hourly":"5","attempts":"5"}') ON CONFLICT DO NOTHING`)
	return e
}
