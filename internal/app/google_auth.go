package app

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// A Google identity is linked only after independent proof of phone ownership.
// Google emails never grant a role and are never used to merge accounts.
func googleConfigured() bool {
	u, e := url.Parse(os.Getenv("GOOGLE_REDIRECT_URL"))
	return e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Path == "/auth/google/callback" && u.RawQuery == "" && u.Fragment == "" && os.Getenv("GOOGLE_CLIENT_ID") != "" && os.Getenv("GOOGLE_CLIENT_SECRET") != ""
}
func (a *App) googleClient() *http.Client {
	if a.GoogleHTTP != nil {
		return a.GoogleHTTP
	}
	return &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (a *App) googleStart(w http.ResponseWriter, r *http.Request) {
	if !googleConfigured() {
		a.fail(w, r, "ورود با گوگل هنوز توسط مدیریت تنظیم نشده است", 503)
		return
	}
	if sess(r).User != nil {
		a.fail(w, r, "برای ورود به حساب دیگر ابتدا خارج شوید", 409)
		return
	}
	next := safeNext(r.FormValue("next"))
	intent := authIntent(r.FormValue("intent"), next)
	if intent == "management" {
		a.fail(w, r, "مدیریت باید از مسیر اختصاصی وارد شود", 403)
		return
	}
	ref := normalizeReferralCode(r.FormValue("referral_code"))
	if ref != "" {
		var valid bool
		if referralCodeRE.MatchString(ref) {
			a.DB.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM referral_codes WHERE code=$1 AND enabled)", ref).Scan(&valid)
		}
		if !valid {
			a.fail(w, r, "کد معرف معتبر یا فعال نیست", 400)
			return
		}
	}
	state, verifier, nonce := token(), token(), token()
	// Bounded outstanding attempts per browser; no credential/token is logged.
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		a.fail(w, r, "ورود شروع نشد", 500)
		return
	}
	defer tx.Rollback(r.Context())
	_, e = tx.Exec(r.Context(), "SELECT token FROM sessions WHERE token=$1 FOR UPDATE", sess(r).Token)
	var n int
	if e == nil {
		e = tx.QueryRow(r.Context(), "SELECT count(*) FROM google_login_attempts WHERE session_token=$1 AND expires_at>now()", sess(r).Token).Scan(&n)
	}
	if n >= 10 {
		a.fail(w, r, "برای تلاش دوباره کمی صبر کنید", 429)
		return
	}
	if e == nil {
		_, e = tx.Exec(r.Context(), "INSERT INTO google_login_attempts(state,session_token,verifier,nonce,next_path,intent,referral_code) VALUES($1,$2,$3,$4,$5,$6,$7)", state, sess(r).Token, verifier, nonce, next, intent, ref)
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		a.fail(w, r, "ورود شروع نشد", 500)
		return
	}
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {os.Getenv("GOOGLE_CLIENT_ID")}, "redirect_uri": {os.Getenv("GOOGLE_REDIRECT_URL")}, "response_type": {"code"}, "scope": {"openid email profile"}, "state": {state}, "nonce": {nonce}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "prompt": {"select_account"}}
	destination := "https://accounts.google.com/o/oauth2/v2/auth?" + q.Encode()
	if r.Header.Get("HX-Request") == "true" {
		redirect(w, r, destination)
		return
	}
	p := a.page(r, "ادامهٔ ورود با گوگل")
	p.Content = destination
	a.render(w, r, "google-continue", p)
}

type googleClaims struct {
	AuthorizedParty string `json:"azp"`
	Issuer          string `json:"iss"`
	Subject         string `json:"sub"`
	Audience        string `json:"aud"`
	Email           string `json:"email"`
	Verified        bool   `json:"email_verified"`
	Name            string `json:"name"`
	Nonce           string `json:"nonce"`
	Expiry          int64  `json:"exp"`
	Issued          int64  `json:"iat"`
}

func (a *App) googleJSON(r *http.Request, target any) error {
	res, e := a.googleClient().Do(r)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("google response rejected")
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(target)
}
func (a *App) verifyGoogleToken(ctx context.Context, raw, nonce string) (googleClaims, error) {
	var c googleClaims
	bad := errors.New("invalid Google identity")
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return c, bad
	}
	header, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return c, bad
	}
	var h struct{ Alg, Kid string }
	if json.Unmarshal(header, &h) != nil || h.Alg != "RS256" || h.Kid == "" {
		return c, bad
	}
	body, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil || json.Unmarshal(body, &c) != nil {
		return c, bad
	}
	now := time.Now().Unix()
	if !contains([]string{"https://accounts.google.com", "accounts.google.com"}, c.Issuer) || c.Audience != os.Getenv("GOOGLE_CLIENT_ID") || (c.AuthorizedParty != "" && c.AuthorizedParty != os.Getenv("GOOGLE_CLIENT_ID")) || c.Nonce != nonce || c.Subject == "" || len(c.Subject) > 255 || !c.Verified || c.Email == "" || len(c.Email) > 320 || c.Issued <= 0 || c.Expiry <= now || c.Issued > now+60 || c.Expiry < c.Issued {
		return c, bad
	}
	var keys struct {
		Keys []struct{ Kid, Kty, Alg, Use, N, E string }
	}
	req, e := http.NewRequestWithContext(ctx, "GET", "https://www.googleapis.com/oauth2/v3/certs", nil)
	if e != nil {
		return c, bad
	}
	if a.googleJSON(req, &keys) != nil {
		return c, bad
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[2])
	if e != nil {
		return c, bad
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	for _, k := range keys.Keys {
		if k.Kid != h.Kid || k.Kty != "RSA" || k.Alg != "RS256" || k.Use != "sig" {
			continue
		}
		nb, ne := base64.RawURLEncoding.DecodeString(k.N)
		eb, ee := base64.RawURLEncoding.DecodeString(k.E)
		if ne != nil || ee != nil || len(eb) > 4 || len(eb) == 0 || len(nb) < 256 || len(nb) > 1024 {
			continue
		}
		exp := 0
		for _, v := range eb {
			exp = exp*256 + int(v)
		}
		if exp < 3 || exp%2 == 0 {
			continue
		}
		if rsa.VerifyPKCS1v15(&rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: exp}, crypto.SHA256, sum[:], sig) == nil {
			return c, nil
		}
	}
	return c, bad
}
func (a *App) googleCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !googleConfigured() {
		a.fail(w, r, "ورود گوگل تنظیم نشده است", 503)
		return
	}
	if sess(r).User != nil {
		a.fail(w, r, "برای تغییر حساب ابتدا خارج شوید", 409)
		return
	}
	ctx := r.Context()
	state := r.URL.Query().Get("state")
	var verifier, nonce string
	p := a.page(r, "تأیید شماره موبایل حساب گوگل")
	p.GoogleReady = true
	// Consume before network calls, so a replay cannot exchange the code twice.
	e := a.DB.QueryRow(ctx, `UPDATE google_login_attempts SET consumed=true WHERE state=$1 AND session_token=$2 AND NOT consumed AND subject='' AND expires_at>now() RETURNING verifier,nonce,next_path,intent,referral_code`, state, sess(r).Token).Scan(&verifier, &nonce, &p.Next, &p.AuthIntent, &p.ReferralCode)
	if e != nil || r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
		a.fail(w, r, "ورود گوگل لغو یا منقضی شد؛ دوباره شروع کنید", 400)
		return
	}
	q := url.Values{"client_id": {os.Getenv("GOOGLE_CLIENT_ID")}, "client_secret": {os.Getenv("GOOGLE_CLIENT_SECRET")}, "code": {r.URL.Query().Get("code")}, "code_verifier": {verifier}, "grant_type": {"authorization_code"}, "redirect_uri": {os.Getenv("GOOGLE_REDIRECT_URL")}}
	req, e := http.NewRequestWithContext(ctx, "POST", "https://oauth2.googleapis.com/token", strings.NewReader(q.Encode()))
	if e != nil {
		a.fail(w, r, "ورود گوگل انجام نشد", 502)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if a.googleJSON(req, &tok) != nil {
		a.fail(w, r, "ارتباط با گوگل انجام نشد؛ دوباره تلاش کنید", 502)
		return
	}
	c, e := a.verifyGoogleToken(ctx, tok.IDToken, nonce)
	if e != nil {
		a.fail(w, r, "هویت حساب گوگل تأیید نشد", 403)
		return
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ورود انجام نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	var u User
	e = tx.QueryRow(ctx, "SELECT u.id,u.phone,u.name,u.role FROM google_identities g JOIN users u ON u.id=g.user_id WHERE g.subject=$1", c.Subject).Scan(&u.ID, &u.Phone, &u.Name, &u.Role)
	if errors.Is(e, pgx.ErrNoRows) {
		p.GoogleAttempt = token()
		p.GoogleEmail = c.Email
		_, e = tx.Exec(ctx, `INSERT INTO google_login_attempts(state,session_token,verifier,nonce,next_path,intent,referral_code,subject,email,name) VALUES($1,$2,'','',$3,$4,$5,$6,$7,$8)`, p.GoogleAttempt, sess(r).Token, p.Next, p.AuthIntent, p.ReferralCode, c.Subject, c.Email, c.Name)
		if e == nil {
			e = tx.Commit(ctx)
		}
		if e != nil {
			a.fail(w, r, "ثبت ورود انجام نشد", 500)
			return
		}
		a.renderAuth(w, r, "login", p)
		return
	}
	if e != nil {
		a.fail(w, r, "ورود انجام نشد", 500)
		return
	}
	if u.Role != "owner" && u.Role != "provider" {
		a.fail(w, r, "این حساب باید از مسیر ورود نقش خود وارد شود", 403)
		return
	}
	if _, e = tx.Exec(ctx, "UPDATE google_identities SET email=$2 WHERE subject=$1", c.Subject, c.Email); e != nil {
		a.fail(w, r, "ورود انجام نشد", 500)
		return
	}
	nt, csrf := token(), token()
	_, e = tx.Exec(ctx, "INSERT INTO sessions(token,user_id,csrf,expires_at) VALUES($1,$2,$3,now()+interval '7 days')", nt, u.ID, csrf)
	if e == nil {
		_, e = tx.Exec(ctx, "UPDATE bookings SET owner_id=$1,session_token=$2 WHERE session_token=$3 AND owner_id IS NULL AND status='draft'", u.ID, nt, sess(r).Token)
	}
	if e == nil {
		_, e = tx.Exec(ctx, "UPDATE sessions SET expires_at=now() WHERE token=$1", sess(r).Token)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "ورود انجام نشد", 500)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "keepet_session", Value: nt, Path: "/", HttpOnly: true, Secure: secureSessionCookie(r), SameSite: http.SameSiteLaxMode, MaxAge: 604800})
	next := p.Next
	if next == "/dashboard" && u.Role == "provider" {
		next = "/provider"
	}
	if u.Role == "owner" && p.AuthIntent == "provider" && (next == "/dashboard" || next == "/provider") {
		next = "/join"
	}
	redirect(w, r, next)
}
func linkGoogleIdentity(ctx context.Context, tx pgx.Tx, state, session string, u User) error {
	if u.Role != "owner" && u.Role != "provider" {
		return errors.New("role not eligible")
	}
	var subject, email string
	e := tx.QueryRow(ctx, "UPDATE google_login_attempts SET consumed=true WHERE state=$1 AND session_token=$2 AND subject<>'' AND NOT consumed AND expires_at>now() RETURNING subject,email", state, session).Scan(&subject, &email)
	if e != nil {
		return e
	}
	// Unique subject and user_id forbid silent reassignment or replacing a previous identity.
	_, e = tx.Exec(ctx, "INSERT INTO google_identities(subject,user_id,email) VALUES($1,$2,$3) ON CONFLICT(subject) DO UPDATE SET email=excluded.email WHERE google_identities.user_id=excluded.user_id", subject, u.ID, email)
	if e != nil {
		return e
	}
	var same bool
	e = tx.QueryRow(ctx, "SELECT user_id=$2 FROM google_identities WHERE subject=$1", subject, u.ID).Scan(&same)
	if e != nil {
		return e
	}
	if !same {
		return errors.New("identity already linked")
	}
	return nil
}
