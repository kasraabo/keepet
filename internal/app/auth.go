package app

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var phoneRE = regexp.MustCompile(`^09[0-9]{9}$`)

func safeNext(s string) string {
	if strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") && !strings.ContainsAny(s, "\\\r\n") {
		return s
	}
	return "/dashboard"
}
func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "ورود به کیپت")
	p.Next = safeNext(r.URL.Query().Get("next"))
	p.AuthIntent = "owner"
	p.GoogleReady = googleConfigured()
	if p.Next == "/join" || strings.HasPrefix(p.Next, "/provider") {
		p.AuthIntent = "provider"
	}
	p.ReferralCode = normalizeReferralCode(r.URL.Query().Get("ref"))
	if len(p.ReferralCode) > 40 {
		p.ReferralCode = ""
	}
	a.renderAuth(w, r, "login", p)
}
func (a *App) requestOTP(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "کد ورود")
	p.Next = safeNext(r.FormValue("next"))
	p.ReferralCode = normalizeReferralCode(r.FormValue("referral_code"))
	p.AuthIntent = authIntent(r.FormValue("intent"), p.Next)
	p.GoogleReady = googleConfigured()
	p.GoogleAttempt = r.FormValue("google_attempt")
	if p.GoogleAttempt != "" {
		var next, intent, ref string
		e := a.DB.QueryRow(r.Context(), "SELECT next_path,intent,referral_code,email FROM google_login_attempts WHERE state=$1 AND session_token=$2 AND subject<>'' AND NOT consumed AND expires_at>now()", p.GoogleAttempt, sess(r).Token).Scan(&next, &intent, &ref, &p.GoogleEmail)
		if e != nil {
			a.fail(w, r, "ورود گوگل منقضی شده؛ دوباره شروع کنید", 409)
			return
		}
		p.Next, p.AuthIntent, p.ReferralCode = next, intent, ref
	}
	policy := a.otpPolicy(r.Context())
	phone := normalize(r.FormValue("phone"))
	if !phoneRE.MatchString(phone) {
		p.Error = "شمارهٔ موبایل را به صورت ۰۹۱۲۱۲۳۴۵۶۷ وارد کنید."
		a.renderAuth(w, r, "login", p)
		return
	}
	if p.ReferralCode != "" {
		var valid bool
		if referralCodeRE.MatchString(p.ReferralCode) {
			a.DB.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM referral_codes WHERE code=$1 AND enabled)", p.ReferralCode).Scan(&valid)
		}
		if !valid {
			p.Error = "کد معرف معتبر یا فعال نیست؛ آن را اصلاح یا خالی کنید."
			a.renderAuth(w, r, "login", p)
			return
		}
	}
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		a.fail(w, r, "خطای دیتابیس", 500)
		return
	}
	defer tx.Rollback(ctx)
	// Lock the session then phone to serialize throttling across devices and numbers.
	if _, err = tx.Exec(ctx, "SELECT token FROM sessions WHERE token=$1 FOR UPDATE", sess(r).Token); err != nil {
		a.fail(w, r, "خطای نشست", 500)
		return
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", phone); err != nil {
		a.fail(w, r, "خطای محدودیت ورود", 500)
		return
	}
	var count, sessionCount int
	var last *time.Time
	err = tx.QueryRow(r.Context(), "SELECT count(*),max(created_at) FROM otp_challenges WHERE phone=$1 AND created_at>now()-interval '1 hour'", phone).Scan(&count, &last)
	if err != nil {
		a.fail(w, r, "خطا در ایجاد کد ورود", 500)
		return
	}
	tx.QueryRow(r.Context(), "SELECT count(*) FROM otp_challenges WHERE session_token=$1 AND created_at>now()-interval '1 hour'", sess(r).Token).Scan(&sessionCount)
	if count >= policy.Hourly || sessionCount >= 10 || last != nil && time.Since(*last) < time.Duration(policy.Resend)*time.Second {
		p.Error = "برای ارسال دوباره کمی صبر کنید. حداکثر " + digits(fmt.Sprint(policy.Hourly)) + " کد در ساعت مجاز است."
		a.renderAuth(w, r, "login", p)
		return
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(math.Pow10(policy.Digits))))
	if err != nil {
		a.fail(w, r, "خطای ایجاد کد", 500)
		return
	}
	p.DevCode = fmt.Sprintf("%0*d", policy.Digits, n.Int64())
	p.Challenge = token()
	_, err = tx.Exec(r.Context(), "INSERT INTO otp_challenges(id,session_token,phone,code_hash,expires_at,max_attempts,code_digits,referral_code,next_path,login_intent,google_attempt) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,''))", p.Challenge, sess(r).Token, phone, hash(p.Challenge+p.DevCode), time.Now().Add(time.Duration(policy.TTL)*time.Second), policy.Attempts, policy.Digits, p.ReferralCode, p.Next, p.AuthIntent, p.GoogleAttempt)
	if err != nil {
		a.fail(w, r, "ذخیرهٔ کد ورود انجام نشد", 500)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.fail(w, r, "کد ذخیره نشد", 500)
		return
	}

	p.AuthNew = true
	a.DB.QueryRow(ctx, "SELECT NOT EXISTS(SELECT 1 FROM users WHERE phone=$1)", phone).Scan(&p.AuthNew)
	a.renderAuth(w, r, "otp", p)
}
func (a *App) verifyOTP(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "تأیید ورود")
	p.Next = safeNext(r.FormValue("next"))
	p.Challenge = r.FormValue("challenge")
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		a.fail(w, r, "خطای دیتابیس", 500)
		return
	}
	defer tx.Rollback(ctx)
	var phone, h string
	var attempts, maxAttempts int
	var expires time.Time
	var consumed bool
	err = tx.QueryRow(ctx, "SELECT phone,code_hash,attempts,expires_at,consumed,max_attempts,code_digits,referral_code,next_path,login_intent,COALESCE(google_attempt,'') FROM otp_challenges WHERE id=$1 AND session_token=$2 FOR UPDATE", p.Challenge, sess(r).Token).Scan(&phone, &h, &attempts, &expires, &consumed, &maxAttempts, &p.CodeDigits, &p.ReferralCode, &p.Next, &p.AuthIntent, &p.GoogleAttempt)
	if err != nil || consumed || attempts >= maxAttempts || time.Now().After(expires) {
		p.Error = "کد منقضی یا مصرف شده است. کد تازه درخواست کنید."
		a.renderAuth(w, r, "login", p)
		return
	}
	p.AuthNew = true
	if e := tx.QueryRow(ctx, "SELECT NOT EXISTS(SELECT 1 FROM users WHERE phone=$1)", phone).Scan(&p.AuthNew); e != nil {
		a.fail(w, r, "بررسی حساب انجام نشد", 500)
		return
	}
	if subtle.ConstantTimeCompare([]byte(h), []byte(hash(p.Challenge+normalize(r.FormValue("code"))))) != 1 {
		tx.Exec(ctx, "UPDATE otp_challenges SET attempts=attempts+1 WHERE id=$1", p.Challenge)
		tx.Commit(ctx)
		p.Error = "کد درست نیست. دوباره امتحان کنید."
		a.renderAuth(w, r, "otp", p)
		return
	}
	var u User
	newOwner := false
	err = tx.QueryRow(ctx, "SELECT id,phone,name,role FROM users WHERE phone=$1", phone).Scan(&u.ID, &u.Phone, &u.Name, &u.Role)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		a.fail(w, r, "بررسی حساب انجام نشد", 500)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if r.FormValue("terms") != "on" {
			p.Error = "پذیرش قوانین برای ساخت حساب لازم است."
			a.renderAuth(w, r, "otp", p)
			return
		}
		newOwner = true
		u = User{ID: token(), Phone: phone, Name: strings.TrimSpace(r.FormValue("name")), Role: "owner"}
		if p.GoogleAttempt != "" && u.Name == "" {
			if e := tx.QueryRow(ctx, "SELECT name FROM google_login_attempts WHERE state=$1 AND session_token=$2 AND NOT consumed AND expires_at>now()", p.GoogleAttempt, sess(r).Token).Scan(&u.Name); e != nil {
				a.fail(w, r, "ورود گوگل منقضی شد", 409)
				return
			}
		}
		if len(u.Name) > 200 {
			a.fail(w, r, "نام خیلی طولانی است", 400)
			return
		}
		err = tx.QueryRow(ctx, "INSERT INTO users(id,phone,name,role) VALUES($1,$2,$3,'owner') ON CONFLICT(phone) DO NOTHING RETURNING id", u.ID, u.Phone, u.Name).Scan(&u.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			newOwner = false
			err = tx.QueryRow(ctx, "SELECT id,phone,name,role FROM users WHERE phone=$1", phone).Scan(&u.ID, &u.Phone, &u.Name, &u.Role)
		}
		if err != nil {
			a.fail(w, r, "ساخت حساب انجام نشد", 500)
			return
		}
		tx.QueryRow(ctx, "SELECT id,role FROM users WHERE phone=$1", phone).Scan(&u.ID, &u.Role)
	}
	if newOwner && p.ReferralCode != "" {
		_, err = tx.Exec(ctx, "SELECT referral_register($1,$2)", u.ID, p.ReferralCode)
		if err != nil {
			_ = tx.Rollback(ctx)
			p.Error = "کد معرف دیگر فعال نیست یا پاداش قابل ثبت نیست؛ کد را بررسی کنید و دوباره وارد شوید."
			a.renderAuth(w, r, "login", p)
			return
		}
	}
	if strings.HasPrefix(p.Next, "/management") && u.Role != "admin" {
		a.fail(w, r, "این حساب دسترسی مدیریت پلتفرم ندارد", 403)
		return
	}
	if p.GoogleAttempt != "" {
		if e := linkGoogleIdentity(ctx, tx, p.GoogleAttempt, sess(r).Token, u); e != nil {
			a.fail(w, r, "این حساب گوگل قابل اتصال نیست؛ با شماره موبایل وارد شوید یا اتصال قبلی را بررسی کنید", 409)
			return
		}
	}
	newToken, newCSRF := token(), token()
	_, err = tx.Exec(ctx, "INSERT INTO sessions(token,user_id,csrf,expires_at) VALUES($1,$2,$3,$4)", newToken, u.ID, newCSRF, time.Now().Add(7*24*time.Hour))
	if err == nil {
		_, err = tx.Exec(ctx, "UPDATE bookings SET owner_id=$1,session_token=$2 WHERE session_token=$3 AND owner_id IS NULL AND status='draft'", u.ID, newToken, sess(r).Token)
	}
	if err == nil {
		_, err = tx.Exec(ctx, "UPDATE otp_challenges SET consumed=true WHERE id=$1", p.Challenge)
	}
	if err == nil {
		_, err = tx.Exec(ctx, "UPDATE sessions SET expires_at=now() WHERE token=$1", sess(r).Token)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		a.fail(w, r, "ورود انجام نشد", 500)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "keepet_session", Value: newToken, Path: "/", HttpOnly: true, Secure: secureSessionCookie(r), SameSite: http.SameSiteLaxMode, MaxAge: 604800})
	next := p.Next
	if next == "/dashboard" {
		if u.Role == "ops" {
			next = "/ops"
		}
		if u.Role == "admin" {
			next = "/admin"
		}
		if u.Role == "content" {
			next = "/content"
		}
		if u.Role == "provider" {
			next = "/provider"
		}
	}
	if p.AuthIntent == "provider" && u.Role == "owner" && (next == "/dashboard" || next == "/provider") {
		next = "/join"
	}
	if newOwner && p.AuthIntent != "provider" {
		next = "/profile?next=" + url.QueryEscape(next)
	}
	redirect(w, r, next)
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	a.DB.Exec(r.Context(), "UPDATE sessions SET expires_at=now() WHERE token=$1", sess(r).Token)
	http.SetCookie(w, &http.Cookie{Name: "keepet_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	redirect(w, r, "/")
}
func noticeURL(path, msg string) string {
	u, err := url.Parse(path)
	if err != nil {
		return "/dashboard"
	}
	q := u.Query()
	q.Set("notice", msg)
	u.RawQuery = q.Encode()
	return u.String()
}

func authIntent(intent, next string) string {
	if strings.HasPrefix(next, "/management") {
		return "management"
	}
	if intent == "provider" || next == "/join" || strings.HasPrefix(next, "/provider") {
		return "provider"
	}
	return "owner"
}
func (a *App) providerLogin(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "ورود متخصصان کیپت")
	p.AuthIntent = "provider"
	p.Next = safeNext(r.URL.Query().Get("next"))
	if p.Next == "/dashboard" {
		p.Next = "/provider"
	}
	p.GoogleReady = googleConfigured()
	p.ReferralCode = normalizeReferralCode(r.URL.Query().Get("ref"))
	a.renderAuth(w, r, "login", p)
}

func secureSessionCookie(r *http.Request) bool {
	return r.TLS != nil || os.Getenv("SESSION_COOKIE_SECURE") == "true"
}
