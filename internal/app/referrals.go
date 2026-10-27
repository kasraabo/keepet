package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type ReferralSettings struct {
	Revision                                                            int64
	Enabled, ReferrerBonusEnabled, InviteeBonusEnabled, ProviderEnabled bool
	ReferrerBonus, InviteeBonus                                         int64
	VetBPS, GroomingBPS, SamplingBPS                                    int
	Basis                                                               string
}
type ReferralCode struct {
	UserID, Code, Name, Phone string
	Enabled                   bool
	Revision                  int64
}
type ReferralPerson struct {
	ID, ReferrerID, Name, Phone, Role, Service, Referrer, Code string
	Enabled                                                    bool
	Revision                                                   int64
	Joined                                                     time.Time
	Orders                                                     int
	Cash, Bonus, Welcome                                       int64
}
type ReferralEvent struct {
	ID                        int64
	Name, Kind, BookingID     string
	Amount, Cash, Bonus, Debt int64
	At                        time.Time
}
type ReferralPage struct {
	Settings                                        ReferralSettings
	Code                                            ReferralCode
	Admin                                           bool
	Query                                           string
	People                                          []ReferralPerson
	Events                                          []ReferralEvent
	Codes                                           []ReferralCode
	Members, Providers                              int
	Earned, Reversed, Bonus, Welcome, Pending, Debt int64
	Prev, Next, EventsPrev, EventsNext              string
}

var referralCodeRE = regexp.MustCompile(`^KP-[A-F0-9]{12}$`)

func normalizeReferralCode(s string) string { return strings.ToUpper(strings.TrimSpace(normalize(s))) }
func referralPercent(n int) string {
	v := strconv.Itoa(n / 100)
	if n%100 != 0 {
		v += "٫" + strings.TrimRight(fmt.Sprintf("%02d", n%100), "0")
	}
	return digits(v) + "٪"
}
func referralKind(s string) string {
	switch s {
	case "signup_referrer":
		return "هدیهٔ عضویت فرد معرفی‌شده"
	case "signup_welcome":
		return "اعتبار خوش‌آمدگویی"
	case "order_income":
		return "درآمد معرفی متخصص"
	case "order_reversal":
		return "اصلاح درآمد پس از عودت وجه"
	case "debt_recovery":
		return "وصول بدهی اصلاح پاداش"
	}
	return s
}
func (a *App) referralSettings(ctx context.Context) (ReferralSettings, error) {
	var s ReferralSettings
	e := a.DB.QueryRow(ctx, `SELECT revision,enabled,referrer_bonus_enabled,invitee_bonus_enabled,provider_enabled,referrer_bonus,invitee_bonus,vet_bps,grooming_bps,sampling_bps,basis FROM referral_settings WHERE id=1`).Scan(&s.Revision, &s.Enabled, &s.ReferrerBonusEnabled, &s.InviteeBonusEnabled, &s.ProviderEnabled, &s.ReferrerBonus, &s.InviteeBonus, &s.VetBPS, &s.GroomingBPS, &s.SamplingBPS, &s.Basis)
	return s, e
}
func (a *App) ensureReferralCode(ctx context.Context, uid string) (ReferralCode, error) {
	var c ReferralCode
	for i := 0; i < 5; i++ {
		_, e := a.DB.Exec(ctx, "INSERT INTO referral_codes(user_id,code) VALUES($1,$2) ON CONFLICT DO NOTHING", uid, "KP-"+strings.ToUpper(token()[:12]))
		if e != nil {
			return c, e
		}
		e = a.DB.QueryRow(ctx, "SELECT user_id,code,enabled,revision FROM referral_codes WHERE user_id=$1", uid).Scan(&c.UserID, &c.Code, &c.Enabled, &c.Revision)
		if e == nil {
			return c, nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return c, e
		}
	}
	return c, fmt.Errorf("code allocation failed")
}
func referralPagination(r *http.Request, key string) (int, func(int) string) {
	n, _ := strconv.Atoi(normalize(r.URL.Query().Get(key)))
	if n < 1 {
		n = 1
	}
	if n > 10000 {
		n = 10000
	}
	return n, func(next int) string {
		q := r.URL.Query()
		q.Set(key, strconv.Itoa(next))
		return r.URL.Path + "?" + q.Encode()
	}
}
func (a *App) referralsPage(w http.ResponseWriter, r *http.Request) {
	admin := strings.HasPrefix(r.URL.Path, "/management/")
	if admin {
		if !a.auth(w, r, "admin") {
			return
		}
	} else if !a.auth(w, r) {
		return
	}
	ctx := r.Context()
	p := a.page(r, "دعوت دوستان و درآمد معرفی")
	v := &ReferralPage{Admin: admin, Query: strings.TrimSpace(r.URL.Query().Get("q"))}
	p.Referrals = v
	if len(v.Query) > 200 {
		a.fail(w, r, "عبارت جست‌وجو بیش از حد طولانی است", 400)
		return
	}
	var e error
	v.Settings, e = a.referralSettings(ctx)
	if e != nil {
		a.fail(w, r, "تنظیمات معرفی بارگذاری نشد", 500)
		return
	}
	v.Code, e = a.ensureReferralCode(ctx, p.User.ID)
	if e != nil {
		a.fail(w, r, "کد معرف ساخته نشد", 500)
		return
	}
	uid := p.User.ID
	if admin {
		uid = ""
	}
	e = a.DB.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE EXISTS(SELECT 1 FROM providers p WHERE p.user_id=r.invitee_id AND p.verified)) FROM referrals r WHERE ($1='' OR r.referrer_id=$1)`, uid).Scan(&v.Members, &v.Providers)
	if e != nil {
		a.fail(w, r, "آمار دعوت‌ها خوانده نشد", 500)
		return
	}
	e = a.DB.QueryRow(ctx, `SELECT COALESCE(sum(amount) FILTER(WHERE kind='order_income'),0),COALESCE(sum(amount) FILTER(WHERE kind='order_reversal'),0),COALESCE(sum(amount) FILTER(WHERE kind='signup_referrer'),0),COALESCE(sum(amount) FILTER(WHERE kind='signup_welcome'),0) FROM referral_events WHERE ($1='' OR user_id=$1)`, uid).Scan(&v.Earned, &v.Reversed, &v.Bonus, &v.Welcome)
	if e == nil {
		e = a.DB.QueryRow(ctx, "SELECT COALESCE(sum(amount),0) FROM referral_order_awards WHERE status='pending' AND ($1='' OR referrer_id=$1)", uid).Scan(&v.Pending)
	}
	if e == nil {
		e = a.DB.QueryRow(ctx, "SELECT COALESCE(sum(debt),0) FROM referral_accounts WHERE ($1='' OR user_id=$1)", uid).Scan(&v.Debt)
	}
	if e != nil {
		a.fail(w, r, "آمار پاداش‌ها خوانده نشد", 500)
		return
	}
	page, pageURL := referralPagination(r, "page")
	ep, epURL := referralPagination(r, "events_page")
	query := ""
	if admin {
		query = v.Query
	}
	rows, e := a.DB.Query(ctx, `SELECT r.invitee_id,r.referrer_id,u.name,CASE WHEN $1='' THEN u.phone ELSE '' END,u.role,COALESCE(p.service,''),parent.name,r.code,r.enabled,r.revision,r.created_at,
 (SELECT count(*) FROM bookings b WHERE b.payment_status='paid' AND booking_settlement_ready(b) AND ((p.id IS NOT NULL AND b.provider_id=p.id) OR (p.id IS NULL AND b.owner_id=u.id))),
 COALESCE((SELECT sum(credited-adjusted) FROM referral_order_awards a WHERE a.provider_user_id=u.id AND a.referrer_id=r.referrer_id),0),r.referrer_bonus,r.invitee_bonus
 FROM referrals r JOIN users u ON u.id=r.invitee_id JOIN users parent ON parent.id=r.referrer_id LEFT JOIN providers p ON p.user_id=u.id
 WHERE ($1='' OR r.referrer_id=$1) AND ($2='' OR u.phone=$2 OR r.code ILIKE '%'||$2||'%' OR u.name ILIKE '%'||$2||'%' OR parent.name ILIKE '%'||$2||'%')
 ORDER BY r.created_at DESC,r.invitee_id LIMIT 26 OFFSET $3`, uid, query, (page-1)*25)
	if e != nil {
		a.fail(w, r, "فهرست دعوت‌ها خوانده نشد", 500)
		return
	}
	for rows.Next() {
		var person ReferralPerson
		e = rows.Scan(&person.ID, &person.ReferrerID, &person.Name, &person.Phone, &person.Role, &person.Service, &person.Referrer, &person.Code, &person.Enabled, &person.Revision, &person.Joined, &person.Orders, &person.Cash, &person.Bonus, &person.Welcome)
		if e != nil {
			break
		}
		v.People = append(v.People, person)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		a.fail(w, r, "فهرست دعوت‌ها خوانده نشد", 500)
		return
	}
	if len(v.People) > 25 {
		v.People = v.People[:25]
		v.Next = pageURL(page + 1)
	}
	if page > 1 {
		v.Prev = pageURL(page - 1)
	}
	rows, e = a.DB.Query(ctx, `SELECT e.id,u.name,e.kind,COALESCE(e.booking_id,''),e.amount,e.cash_delta,e.bonus_delta,e.debt_delta,e.created_at FROM referral_events e JOIN users u ON u.id=e.user_id WHERE ($1='' OR e.user_id=$1) AND ($2='' OR u.phone=$2 OR u.name ILIKE '%'||$2||'%' OR EXISTS(SELECT 1 FROM referrals rr WHERE rr.invitee_id=e.invitee_id AND rr.code ILIKE '%'||$2||'%')) ORDER BY e.id DESC LIMIT 26 OFFSET $3`, uid, query, (ep-1)*25)
	if e != nil {
		a.fail(w, r, "تاریخچهٔ پاداش‌ها خوانده نشد", 500)
		return
	}
	for rows.Next() {
		var ev ReferralEvent
		e = rows.Scan(&ev.ID, &ev.Name, &ev.Kind, &ev.BookingID, &ev.Amount, &ev.Cash, &ev.Bonus, &ev.Debt, &ev.At)
		if e != nil {
			break
		}
		v.Events = append(v.Events, ev)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		a.fail(w, r, "تاریخچهٔ پاداش‌ها خوانده نشد", 500)
		return
	}
	if len(v.Events) > 25 {
		v.Events = v.Events[:25]
		v.EventsNext = epURL(ep + 1)
	}
	if ep > 1 {
		v.EventsPrev = epURL(ep - 1)
	}
	if admin {
		rows, e = a.DB.Query(ctx, `SELECT c.user_id,c.code,u.name,u.phone,c.enabled,c.revision FROM referral_codes c JOIN users u ON u.id=c.user_id WHERE ($1='' OR u.phone=$1 OR c.code ILIKE '%'||$1||'%' OR u.name ILIKE '%'||$1||'%') ORDER BY c.created_at DESC,c.user_id LIMIT 100`, query)
		if e != nil {
			a.fail(w, r, "کدهای معرف خوانده نشد", 500)
			return
		}
		for rows.Next() {
			var c ReferralCode
			e = rows.Scan(&c.UserID, &c.Code, &c.Name, &c.Phone, &c.Enabled, &c.Revision)
			if e != nil {
				break
			}
			v.Codes = append(v.Codes, c)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			a.fail(w, r, "کدهای معرف خوانده نشد", 500)
			return
		}
	}
	a.render(w, r, "referrals", p)
}
func (a *App) saveReferralSettings(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	s := ReferralSettings{Enabled: r.FormValue("enabled") == "on", ReferrerBonusEnabled: r.FormValue("referrer_bonus_enabled") == "on", InviteeBonusEnabled: r.FormValue("invitee_bonus_enabled") == "on", ProviderEnabled: r.FormValue("provider_enabled") == "on", Basis: r.FormValue("basis")}
	rev, e := strconv.ParseInt(normalize(r.FormValue("revision")), 10, 64)
	if e != nil || !contains([]string{"commission", "platform_share"}, s.Basis) {
		a.fail(w, r, "نسخه یا مبنای محاسبه معتبر نیست", 400)
		return
	}
	vals := []*int64{&s.ReferrerBonus, &s.InviteeBonus}
	for i, key := range []string{"referrer_bonus", "invitee_bonus"} {
		v, err := strconv.ParseInt(normalizeNumber(r.FormValue(key)), 10, 64)
		if err != nil || v < 0 || v > 1000000000 {
			a.fail(w, r, "پاداش بین صفر تا یک میلیارد تومان باشد", 400)
			return
		}
		*vals[i] = v
	}
	rates := []*int{&s.VetBPS, &s.GroomingBPS, &s.SamplingBPS}
	for i, key := range []string{"vet_bps", "grooming_bps", "sampling_bps"} {
		v, err := strconv.Atoi(normalize(r.FormValue(key)))
		if err != nil || v < 0 || v > 10000 {
			a.fail(w, r, "درصد بین صفر و صد باشد؛ ۱۰۰ واحد یعنی یک درصد", 400)
			return
		}
		*rates[i] = v
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "تنظیمات ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	fail := func(message string, status int) { _ = tx.Rollback(ctx); a.fail(w, r, message, status) }
	var before []byte
	var actual int64
	e = tx.QueryRow(ctx, "SELECT to_jsonb(s),revision FROM referral_settings s WHERE id=1 FOR UPDATE").Scan(&before, &actual)
	if e != nil {
		fail("تنظیمات خوانده نشد", 500)
		return
	}
	if rev != actual {
		fail("تنظیمات تغییر کرده؛ صفحه را تازه کنید", 409)
		return
	}
	s.Revision = actual + 1
	_, e = tx.Exec(ctx, `UPDATE referral_settings SET revision=revision+1,enabled=$1,referrer_bonus_enabled=$2,invitee_bonus_enabled=$3,provider_enabled=$4,referrer_bonus=$5,invitee_bonus=$6,vet_bps=$7,grooming_bps=$8,sampling_bps=$9,basis=$10 WHERE id=1`, s.Enabled, s.ReferrerBonusEnabled, s.InviteeBonusEnabled, s.ProviderEnabled, s.ReferrerBonus, s.InviteeBonus, s.VetBPS, s.GroomingBPS, s.SamplingBPS, s.Basis)
	if e == nil {
		_, e = tx.Exec(ctx, "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,'referral_settings','1',$2,$3)", sess(r).User.ID, before, mustJSON(s))
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		fail("تنظیمات ذخیره نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/management/referrals", "تنظیمات معرفی ذخیره شد؛ نرخ رزروهای قبلاً پرداخت‌شده تغییر نمی‌کند."))
}
func (a *App) toggleReferral(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	table, idcol, kind := "referrals", "invitee_id", "referral_pause"
	if r.PathValue("kind") == "code" {
		table, idcol, kind = "referral_codes", "user_id", "referral_code_pause"
	} else if r.PathValue("kind") != "person" {
		a.fail(w, r, "اقدام نامعتبر", 404)
		return
	}
	id := r.PathValue("id")
	rev, e := strconv.ParseInt(normalize(r.FormValue("revision")), 10, 64)
	if e != nil {
		a.fail(w, r, "نسخه نامعتبر است", 400)
		return
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ثبت انجام نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	fail := func(message string, status int) { _ = tx.Rollback(ctx); a.fail(w, r, message, status) }
	var actual int64
	var before []byte
	e = tx.QueryRow(ctx, "SELECT revision,to_jsonb(t) FROM "+table+" t WHERE "+idcol+"=$1 FOR UPDATE", id).Scan(&actual, &before)
	if e != nil {
		fail("مورد پیدا نشد", 404)
		return
	}
	if rev != actual {
		fail("اطلاعات تغییر کرده؛ صفحه را تازه کنید", 409)
		return
	}
	enabled := r.FormValue("enabled") == "on"
	_, e = tx.Exec(ctx, "UPDATE "+table+" SET enabled=$2,revision=revision+1 WHERE "+idcol+"=$1", id, enabled)
	if e == nil {
		_, e = tx.Exec(ctx, "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,$2,$3,$4,$5)", sess(r).User.ID, kind, id, before, mustJSON(map[string]bool{"enabled": enabled}))
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		fail("ثبت تغییر انجام نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/management/referrals?q="+url.QueryEscape(r.FormValue("q")), "وضعیت معرفی به‌روز شد."))
}
