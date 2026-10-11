package app

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
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
	a.render(w, r, "login", p)
}
func (a *App) requestOTP(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "کد ورود")
	p.Next = safeNext(r.FormValue("next"))
	phone := normalize(r.FormValue("phone"))
	if !phoneRE.MatchString(phone) {
		p.Error = "شمارهٔ موبایل را به صورت ۰۹۱۲۱۲۳۴۵۶۷ وارد کنید."
		a.render(w, r, "login", p)
		return
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
	if count >= 5 || sessionCount >= 10 || last != nil && time.Since(*last) < 30*time.Second {
		p.Error = "برای ارسال دوباره کمی صبر کنید. حداکثر ۵ کد در ساعت مجاز است."
		a.render(w, r, "login", p)
		return
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		a.fail(w, r, "خطای ایجاد کد", 500)
		return
	}
	p.DevCode = fmt.Sprintf("%06d", n.Int64())
	p.Challenge = token()
	_, err = tx.Exec(r.Context(), "INSERT INTO otp_challenges(id,session_token,phone,code_hash,expires_at) VALUES($1,$2,$3,$4,$5)", p.Challenge, sess(r).Token, phone, hash(p.Challenge+p.DevCode), time.Now().Add(2*time.Minute))
	if err != nil {
		a.fail(w, r, "ذخیرهٔ کد ورود انجام نشد", 500)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.fail(w, r, "کد ذخیره نشد", 500)
		return
	}

	a.render(w, r, "otp", p)
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
	var attempts int
	var expires time.Time
	var consumed bool
	err = tx.QueryRow(ctx, "SELECT phone,code_hash,attempts,expires_at,consumed FROM otp_challenges WHERE id=$1 AND session_token=$2 FOR UPDATE", p.Challenge, sess(r).Token).Scan(&phone, &h, &attempts, &expires, &consumed)
	if err != nil || consumed || attempts >= 5 || time.Now().After(expires) {
		p.Error = "کد منقضی یا مصرف شده است. کد تازه درخواست کنید."
		a.render(w, r, "login", p)
		return
	}
	if subtle.ConstantTimeCompare([]byte(h), []byte(hash(p.Challenge+normalize(r.FormValue("code"))))) != 1 {
		tx.Exec(ctx, "UPDATE otp_challenges SET attempts=attempts+1 WHERE id=$1", p.Challenge)
		tx.Commit(ctx)
		p.Error = "کد درست نیست. دوباره امتحان کنید."
		a.render(w, r, "otp", p)
		return
	}
	var u User
	err = tx.QueryRow(ctx, "SELECT id,phone,name,role FROM users WHERE phone=$1", phone).Scan(&u.ID, &u.Phone, &u.Name, &u.Role)
	if err != nil {
		if r.FormValue("terms") != "on" {
			p.Error = "پذیرش قوانین برای ساخت حساب لازم است."
			a.render(w, r, "otp", p)
			return
		}
		u = User{ID: token(), Phone: phone, Name: strings.TrimSpace(r.FormValue("name")), Role: "owner"}
		if len(u.Name) > 200 {
			a.fail(w, r, "نام خیلی طولانی است", 400)
			return
		}
		_, err = tx.Exec(ctx, "INSERT INTO users(id,phone,name,role) VALUES($1,$2,$3,'owner') ON CONFLICT(phone) DO UPDATE SET phone=excluded.phone RETURNING id", u.ID, u.Phone, u.Name)
		if err != nil {
			a.fail(w, r, "ساخت حساب انجام نشد", 500)
			return
		}
		tx.QueryRow(ctx, "SELECT id,role FROM users WHERE phone=$1", phone).Scan(&u.ID, &u.Role)
	}
	newToken, newCSRF := token(), token()
	_, err = tx.Exec(ctx, "INSERT INTO sessions(token,user_id,csrf,expires_at) VALUES($1,$2,$3,$4)", newToken, u.ID, newCSRF, time.Now().Add(7*24*time.Hour))
	if err == nil {
		_, err = tx.Exec(ctx, "UPDATE bookings SET owner_id=$1,session_token=$2 WHERE session_token=$3 AND status='draft'", u.ID, newToken, sess(r).Token)
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
	http.SetCookie(w, &http.Cookie{Name: "keepet_session", Value: newToken, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: 604800})
	next := p.Next
	if next == "/dashboard" {
		if u.Role == "ops" {
			next = "/ops"
		}
		if u.Role == "admin" {
			next = "/admin"
		}
		if u.Role == "provider" {
			next = "/provider"
		}
	}
	redirect(w, r, next)
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	a.DB.Exec(r.Context(), "UPDATE sessions SET expires_at=now() WHERE token=$1", sess(r).Token)
	http.SetCookie(w, &http.Cookie{Name: "keepet_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	redirect(w, r, "/")
}
func noticeURL(path, msg string) string { return path + "?notice=" + url.QueryEscape(msg) }
