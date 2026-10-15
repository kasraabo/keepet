package app

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Wallet struct {
	UserID, Name, Phone   string
	Cash, Bonus, Reserved int64
	Frozen                bool
	Entries               []WalletEntry
	Bank                  *WalletBank
	Withdrawals           []Withdrawal
}
type WalletEntry struct {
	ID                                                          int64
	Kind, Description, BookingID, UserID, Name                  string
	Cash, Bonus, Reserved, CashAfter, BonusAfter, ReservedAfter int64
	At                                                          time.Time
}
type WalletBank struct {
	ID, UserID, Name, Phone, IBAN, Holder, Status, Note string
	Revision                                            int64
}
type Withdrawal struct {
	ID, UserID, Name, Phone, IBAN, Holder, Status, OperatorID, Operator, Reason, SenderName, SenderAccount, Hash string
	Amount                                                                                                       int64
	At                                                                                                           time.Time
	PaidAt                                                                                                       *time.Time
}

func (x Withdrawal) Rial() int64 { return x.Amount * 10 }

type WalletSettings struct {
	Min, Max, Daily int64
	Enabled         bool
}
type WalletPage struct {
	More                        bool
	Account                     Wallet
	Banks                       []WalletBank
	Requests                    []Withdrawal
	Accounts                    []Wallet
	Entries                     []WalletEntry
	Settings                    WalletSettings
	Key, Filter, Query          string
	Page, Count                 int
	Cash, Bonus, Reserved, Paid int64
}

func walletStatus(s string) string {
	m := map[string]string{"pending": "در انتظار بررسی", "processing": "در حال پرداخت بانکی", "paid": "رسید پرداخت ثبت‌شده", "rejected": "ردشده", "cancelled": "لغوشده", "verified": "تأییدشده", "service_income": "درآمد خدمت", "refund": "عودت وجه", "booking_payment": "پرداخت رزرو", "withdrawal_hold": "رزرو مبلغ برداشت", "withdrawal_paid": "برداشت پرداخت‌شده", "withdrawal_release": "آزادسازی برداشت", "admin_cash": "اصلاح موجودی نقدی", "admin_bonus": "اعتبار هدیه", "topup_demo": "شارژ آزمایشی", "referral_reward": "پاداش معرفی"}
	if v, ok := m[s]; ok {
		return v
	}
	return s
}
func walletAmount(v string) (int64, error) {
	n, e := strconv.ParseInt(normalize(strings.TrimSpace(v)), 10, 64)
	if e != nil || n < 1 || n > 1000000000 {
		return 0, errors.New("مبلغ صحیح بین ۱ و ۱٬۰۰۰٬۰۰۰٬۰۰۰ تومان وارد کنید")
	}
	return n, nil
}
func bankName(s string) string {
	s = strings.NewReplacer("ي", "ی", "ك", "ک", "\u200c", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}
func validIBAN(s string) bool {
	if len(s) != 26 || !strings.HasPrefix(s, "IR") {
		return false
	}
	for _, c := range s[2:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	mod := 0
	for _, c := range s[4:] + "1827" + s[2:4] {
		mod = (mod*10 + int(c-'0')) % 97
	}
	return mod == 1
}
func (a *App) walletSettings(ctx context.Context) (WalletSettings, error) {
	var s WalletSettings
	e := a.DB.QueryRow(ctx, "SELECT min_withdrawal,max_withdrawal,daily_limit,withdrawals_enabled FROM wallet_settings WHERE id=1").Scan(&s.Min, &s.Max, &s.Daily, &s.Enabled)
	return s, e
}
func walletPost(ctx context.Context, tx pgx.Tx, user, kind string, cash, bonus, reserved int64, key, booking, actor, desc string) error {
	var b, act any
	if booking != "" {
		b = booking
	}
	if actor != "" {
		act = actor
	}
	var inserted bool
	return tx.QueryRow(ctx, "SELECT wallet_post($1,$2,$3,$4,$5,$6,$7,$8,$9)", user, kind, cash, bonus, reserved, key, b, act, desc).Scan(&inserted)
}
func walletLock(ctx context.Context, tx pgx.Tx, user string) (Wallet, error) {
	w := Wallet{UserID: user}
	_, e := tx.Exec(ctx, "INSERT INTO wallets(user_id) VALUES($1) ON CONFLICT DO NOTHING", user)
	if e != nil {
		return w, e
	}
	e = tx.QueryRow(ctx, "SELECT cash,bonus,reserved,frozen FROM wallets WHERE user_id=$1 FOR UPDATE", user).Scan(&w.Cash, &w.Bonus, &w.Reserved, &w.Frozen)
	return w, e
}
func walletAudit(ctx context.Context, tx pgx.Tx, actor, kind, id string, data any) error {
	raw, e := json.Marshal(data)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "INSERT INTO business_audit(actor_id,kind,target,after_data) VALUES($1,$2,$3,$4)", actor, kind, id, raw)
	return e
}
func (a *App) walletPage(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	p := a.page(r, "کیف پول من")
	v := &WalletPage{Key: token(), Page: 1, Filter: r.URL.Query().Get("kind")}
	if n, e := strconv.Atoi(r.URL.Query().Get("page")); e == nil && n > 0 && n <= 10000 {
		v.Page = n
	}
	offset := (v.Page - 1) * 100
	p.Wallet = v
	ctx := r.Context()
	uid := p.User.ID
	v.Account = Wallet{UserID: uid, Name: p.User.Name, Phone: p.User.Phone}
	e := a.DB.QueryRow(ctx, "SELECT cash,bonus,reserved,frozen FROM wallets WHERE user_id=$1", uid).Scan(&v.Account.Cash, &v.Account.Bonus, &v.Account.Reserved, &v.Account.Frozen)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		a.fail(w, r, "کیف پول بارگذاری نشد", 500)
		return
	}
	v.Settings, e = a.walletSettings(ctx)
	if e != nil {
		a.fail(w, r, "تنظیمات بارگذاری نشد", 500)
		return
	}
	bank := &WalletBank{}
	e = a.DB.QueryRow(ctx, "SELECT id,user_id,iban,holder,status,revision,review_note FROM wallet_bank_accounts WHERE user_id=$1", uid).Scan(&bank.ID, &bank.UserID, &bank.IBAN, &bank.Holder, &bank.Status, &bank.Revision, &bank.Note)
	if e == nil {
		v.Account.Bank = bank
	} else if !errors.Is(e, pgx.ErrNoRows) {
		a.fail(w, r, "شبا بارگذاری نشد", 500)
		return
	}
	v.Account.Entries, e = a.walletEntries(ctx, uid, v.Filter, offset, 100)
	if e == nil {
		v.Account.Withdrawals, e = a.walletWithdrawals(ctx, uid, "", offset)
	}
	if e != nil {
		a.fail(w, r, "سوابق بارگذاری نشد", 500)
		return
	}
	if e = a.DB.QueryRow(ctx, "SELECT GREATEST((SELECT count(*) FROM wallet_entries WHERE user_id=$1 AND ($2='' OR kind=$2)),(SELECT count(*) FROM wallet_withdrawals WHERE user_id=$1))", uid, v.Filter).Scan(&v.Count); e != nil {
		a.fail(w, r, "تعداد سوابق بارگذاری نشد", 500)
		return
	}
	v.More = v.Page*100 < v.Count
	a.render(w, r, "wallet", p)
}
func (a *App) walletEntries(ctx context.Context, uid, kind string, offset, limit int) ([]WalletEntry, error) {
	rows, e := a.DB.Query(ctx, `SELECT e.id,e.user_id,u.name,e.kind,e.cash_delta,e.bonus_delta,e.reserved_delta,e.cash_after,e.bonus_after,e.reserved_after,COALESCE(e.booking_id,''),e.description,e.created_at FROM wallet_entries e JOIN users u ON u.id=e.user_id WHERE ($1='' OR e.user_id=$1) AND ($2='' OR e.kind=$2) ORDER BY e.id DESC LIMIT $3 OFFSET $4`, uid, kind, limit, offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []WalletEntry
	for rows.Next() {
		var x WalletEntry
		e = rows.Scan(&x.ID, &x.UserID, &x.Name, &x.Kind, &x.Cash, &x.Bonus, &x.Reserved, &x.CashAfter, &x.BonusAfter, &x.ReservedAfter, &x.BookingID, &x.Description, &x.At)
		if e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (a *App) walletWithdrawals(ctx context.Context, uid, status string, offset int) ([]Withdrawal, error) {
	rows, e := a.DB.Query(ctx, `SELECT x.id,x.user_id,u.name,u.phone,x.amount,x.iban,x.holder,x.status,COALESCE(x.operator_id,''),COALESCE(o.name,''),x.reason,x.sender_name,x.sender_account,x.receipt_hash,x.created_at,x.paid_at FROM wallet_withdrawals x JOIN users u ON u.id=x.user_id LEFT JOIN users o ON o.id=x.operator_id WHERE ($1='' OR x.user_id=$1) AND ($2='' OR x.status=$2) ORDER BY x.created_at DESC,x.id LIMIT 100 OFFSET $3`, uid, status, offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Withdrawal
	for rows.Next() {
		var x Withdrawal
		e = rows.Scan(&x.ID, &x.UserID, &x.Name, &x.Phone, &x.Amount, &x.IBAN, &x.Holder, &x.Status, &x.OperatorID, &x.Operator, &x.Reason, &x.SenderName, &x.SenderAccount, &x.Hash, &x.At, &x.PaidAt)
		if e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (a *App) walletBankSave(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	ctx := r.Context()
	u := sess(r).User
	iban := strings.ToUpper(strings.ReplaceAll(normalize(r.FormValue("iban")), " ", ""))
	holder := bankName(r.FormValue("holder"))
	if !validIBAN(iban) || len(holder) < 3 || len(holder) > 200 || holder != bankName(u.Name) {
		a.fail(w, r, "شبای معتبر ایرانی و نام کامل مطابق پروفایل لازم است؛ ابتدا نام خود را در پروفایل تکمیل کنید", 400)
		return
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	_, e = walletLock(ctx, tx, u.ID)
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO wallet_bank_accounts(id,user_id,iban,holder) VALUES($1,$2,$3,$4) ON CONFLICT(user_id) DO UPDATE SET iban=EXCLUDED.iban,holder=EXCLUDED.holder,status='pending',revision=wallet_bank_accounts.revision+1,reviewer_id=NULL,reviewed_at=NULL,review_note='',updated_at=now()`, token(), u.ID, iban, holder)
	}
	if e == nil {
		e = walletAudit(ctx, tx, u.ID, "wallet_bank_change", u.ID, map[string]any{"iban_suffix": iban[len(iban)-4:], "verification": "pending"})
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "شبا ذخیره نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/wallet", "شبا برای بررسی دستی مدیر ثبت شد؛ تغییر شبا نیازمند تأیید دوباره است."))
}
func (a *App) walletWithdraw(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	amount, e := walletAmount(r.FormValue("amount"))
	key := r.FormValue("request_key")
	if e != nil || len(key) != 64 {
		a.fail(w, r, "مبلغ یا شناسهٔ درخواست نامعتبر است", 400)
		return
	}
	ctx := r.Context()
	uid := sess(r).User.ID
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	v, e := walletLock(ctx, tx, uid)
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	var oldUID string
	var oldAmount int64
	e = tx.QueryRow(ctx, "SELECT user_id,amount FROM wallet_withdrawals WHERE request_key=$1", key).Scan(&oldUID, &oldAmount)
	if e == nil {
		if oldUID != uid || oldAmount != amount {
			a.fail(w, r, "شناسهٔ تکراری با مبلغ متفاوت", 409)
			return
		}
		redirect(w, r, "/wallet")
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	var s WalletSettings
	e = tx.QueryRow(ctx, "SELECT min_withdrawal,max_withdrawal,daily_limit,withdrawals_enabled FROM wallet_settings WHERE id=1 FOR SHARE").Scan(&s.Min, &s.Max, &s.Daily, &s.Enabled)
	if e != nil {
		a.fail(w, r, "تنظیمات در دسترس نیست", 500)
		return
	}
	if v.Frozen || !s.Enabled || amount < s.Min || amount > s.Max || amount > v.Cash {
		a.fail(w, r, "برداشت غیرفعال است یا مبلغ خارج از حدود و موجودی قابل برداشت است", 409)
		return
	}
	var b WalletBank
	e = tx.QueryRow(ctx, "SELECT id,iban,holder,status,revision FROM wallet_bank_accounts WHERE user_id=$1 FOR SHARE", uid).Scan(&b.ID, &b.IBAN, &b.Holder, &b.Status, &b.Revision)
	var legal string
	if e == nil {
		e = tx.QueryRow(ctx, "SELECT name FROM users WHERE id=$1 FOR SHARE", uid).Scan(&legal)
	}
	if e != nil || b.Status != "verified" || bankName(legal) != bankName(b.Holder) {
		a.fail(w, r, "شبای مطابق نام فعلی شما باید توسط مدیر تأیید شود", 409)
		return
	}
	var used int64
	e = tx.QueryRow(ctx, `SELECT COALESCE(sum(amount),0) FROM wallet_withdrawals WHERE user_id=$1 AND status IN ('pending','processing','paid') AND created_at>=date_trunc('day',now() AT TIME ZONE 'Asia/Tehran') AT TIME ZONE 'Asia/Tehran'`, uid).Scan(&used)
	if e != nil {
		a.fail(w, r, "سقف روزانه بررسی نشد", 500)
		return
	}
	if used+amount > s.Daily {
		a.fail(w, r, "سقف برداشت روزانه پر شده است", 409)
		return
	}
	id := token()
	_, e = tx.Exec(ctx, "INSERT INTO wallet_withdrawals(id,user_id,amount,bank_id,bank_revision,iban,holder,request_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", id, uid, amount, b.ID, b.Revision, b.IBAN, b.Holder, key)
	if e == nil {
		e = walletPost(ctx, tx, uid, "withdrawal_hold", -amount, 0, amount, "hold:"+id, "", uid, "رزرو مبلغ درخواست برداشت "+id[:8])
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "درخواست ثبت نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/wallet", "درخواست برداشت ثبت شد؛ وضعیت جاری و مبلغ رزروشده را در همین صفحه پیگیری کنید."))
}
func (a *App) walletCancel(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	a.walletRelease(w, r, false)
}
func (a *App) walletRelease(w http.ResponseWriter, r *http.Request, staff bool) {
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	var uid, st string
	var amount int64
	id := r.PathValue("id")
	e = tx.QueryRow(ctx, "SELECT user_id,status,amount FROM wallet_withdrawals WHERE id=$1 FOR UPDATE", id).Scan(&uid, &st, &amount)
	if e != nil || (!staff && uid != sess(r).User.ID) {
		a.fail(w, r, "درخواست پیدا نشد", 404)
		return
	}
	if st != "pending" {
		a.fail(w, r, "فقط درخواست در انتظار قابل لغو یا رد است؛ پرداخت در حال انجام را ابتدا با بانک تطبیق دهید", 409)
		return
	}
	reason := strings.TrimSpace(r.FormValue("reason"))
	next := "cancelled"
	back := "/wallet"
	if staff {
		next = "rejected"
		back = "/management/wallet"
		if len(reason) < 5 || len(reason) > 1000 {
			a.fail(w, r, "دلیل رد را کامل کنید", 400)
			return
		}
	}
	e = walletPost(ctx, tx, uid, "withdrawal_release", amount, 0, -amount, "release:"+id, "", sess(r).User.ID, "آزادسازی برداشت: "+reason)
	if e == nil {
		_, e = tx.Exec(ctx, "UPDATE wallet_withdrawals SET status=$2,reason=$3,updated_at=now() WHERE id=$1", id, next, reason)
	}
	if e == nil {
		e = walletAudit(ctx, tx, sess(r).User.ID, "wallet_"+next, id, map[string]any{"amount": amount, "reason": reason})
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "لغو ثبت نشد", 500)
		return
	}
	redirect(w, r, noticeURL(back, "درخواست بسته و مبلغ به موجودی قابل برداشت برگشت."))
}
func (a *App) walletDemoTopup(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	n, e := walletAmount(r.FormValue("amount"))
	key := r.FormValue("request_key")
	if e != nil || len(key) != 64 {
		a.fail(w, r, "مبلغ نامعتبر است", 400)
		return
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	v, e := walletLock(ctx, tx, sess(r).User.ID)
	if e != nil || v.Frozen {
		a.fail(w, r, "کیف پول در دسترس نیست", 409)
		return
	}
	// This application cannot run in production (New enforces APP_MODE). No bank adapter is called.
	e = walletPost(ctx, tx, sess(r).User.ID, "topup_demo", n, 0, 0, "topup:"+key, "", sess(r).User.ID, "شارژ شبیه‌سازی‌شده؛ وجه بانکی دریافت نشده")
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "شارژ ثبت نشد؛ شناسهٔ تکراری با مبلغ متفاوت را بررسی کنید", 409)
		return
	}
	redirect(w, r, noticeURL("/wallet", "شارژ آزمایشی ثبت شد؛ هیچ پولی از بانک دریافت نشده است."))
}
func (a *App) walletCSV(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	uid := sess(r).User.ID
	if strings.HasPrefix(r.URL.Path, "/management/") {
		if !a.auth(w, r, "admin", "ops") {
			return
		}
		uid = strings.TrimSpace(r.URL.Query().Get("user"))
	}
	entries, e := a.walletEntries(r.Context(), uid, r.URL.Query().Get("kind"), 0, 10000)
	if e != nil {
		a.fail(w, r, "خروجی آماده نشد", 500)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="keepet-wallet.csv"`)
	fmt.Fprint(w, "\ufeff")
	c := csv.NewWriter(w)
	c.Write([]string{"id", "user", "name", "kind", "cash_delta_toman", "bonus_delta_toman", "reserved_delta_toman", "cash_after", "bonus_after", "reserved_after", "booking", "description", "time"})
	for _, x := range entries {
		c.Write([]string{fmt.Sprint(x.ID), csvSafe(x.UserID), csvSafe(x.Name), x.Kind, fmt.Sprint(x.Cash), fmt.Sprint(x.Bonus), fmt.Sprint(x.Reserved), fmt.Sprint(x.CashAfter), fmt.Sprint(x.BonusAfter), fmt.Sprint(x.ReservedAfter), x.BookingID, csvSafe(x.Description), x.At.In(a.Location).Format(time.RFC3339)})
	}
	c.Flush()
}
