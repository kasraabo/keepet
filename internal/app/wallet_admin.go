package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) walletAdmin(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin", "ops") {
		return
	}
	p := a.page(r, "مدیریت کیف پول و برداشت")
	v := &WalletPage{Key: token(), Filter: r.URL.Query().Get("status"), Query: strings.TrimSpace(r.URL.Query().Get("user")), Page: 1}
	p.Wallet = v
	if n, e := strconv.Atoi(r.URL.Query().Get("page")); e == nil && n > 0 && n <= 10000 {
		v.Page = n
	}
	offset := (v.Page - 1) * 100
	ctx := r.Context()
	s, e := a.walletSettings(ctx)
	if e != nil {
		a.fail(w, r, "تنظیمات بارگذاری نشد", 500)
		return
	}
	v.Settings = s
	v.Requests, e = a.walletWithdrawals(ctx, v.Query, v.Filter, offset)
	if e != nil {
		a.fail(w, r, "برداشت‌ها بارگذاری نشد", 500)
		return
	}
	e = a.DB.QueryRow(ctx, "SELECT count(*) FROM wallet_withdrawals WHERE ($1='' OR user_id=$1) AND ($2='' OR status=$2)", v.Query, v.Filter).Scan(&v.Count)
	if e == nil {
		e = a.DB.QueryRow(ctx, "SELECT COALESCE(sum(cash),0),COALESCE(sum(bonus),0),COALESCE(sum(reserved),0) FROM wallets").Scan(&v.Cash, &v.Bonus, &v.Reserved)
	}
	if e == nil {
		e = a.DB.QueryRow(ctx, "SELECT COALESCE(sum(amount),0) FROM wallet_withdrawals WHERE status='paid'").Scan(&v.Paid)
	}
	if e != nil {
		a.fail(w, r, "گزارش مالی آماده نشد", 500)
		return
	}
	rows, e := a.DB.Query(ctx, `SELECT b.id,b.user_id,u.name,u.phone,b.iban,b.holder,b.status,b.revision,b.review_note FROM wallet_bank_accounts b JOIN users u ON u.id=b.user_id WHERE ($1='' OR b.user_id=$1) ORDER BY (b.status='pending') DESC,b.updated_at DESC LIMIT 100 OFFSET $2`, v.Query, offset)
	if e != nil {
		a.fail(w, r, "حساب‌ها بارگذاری نشد", 500)
		return
	}
	for rows.Next() {
		var b WalletBank
		if e = rows.Scan(&b.ID, &b.UserID, &b.Name, &b.Phone, &b.IBAN, &b.Holder, &b.Status, &b.Revision, &b.Note); e != nil {
			break
		}
		v.Banks = append(v.Banks, b)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		a.fail(w, r, "حساب‌ها بارگذاری نشد", 500)
		return
	}
	rows, e = a.DB.Query(ctx, `SELECT u.id,u.name,u.phone,COALESCE(w.cash,0),COALESCE(w.bonus,0),COALESCE(w.reserved,0),COALESCE(w.frozen,false) FROM users u LEFT JOIN wallets w ON w.user_id=u.id WHERE ($1='' OR u.id=$1) ORDER BY u.created_at DESC,u.id LIMIT 100 OFFSET $2`, v.Query, offset)
	if e != nil {
		a.fail(w, r, "کیف‌ها بارگذاری نشد", 500)
		return
	}
	for rows.Next() {
		var x Wallet
		if e = rows.Scan(&x.UserID, &x.Name, &x.Phone, &x.Cash, &x.Bonus, &x.Reserved, &x.Frozen); e != nil {
			break
		}
		v.Accounts = append(v.Accounts, x)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		a.fail(w, r, "کیف‌ها بارگذاری نشد", 500)
		return
	}
	v.Entries, e = a.walletEntries(ctx, v.Query, "", offset, 100)
	if e != nil {
		a.fail(w, r, "دفتر بارگذاری نشد", 500)
		return
	}
	a.render(w, r, "wallet-admin", p)
}
func (a *App) walletBankReview(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin", "ops") {
		return
	}
	st := r.FormValue("status")
	note := strings.TrimSpace(r.FormValue("note"))
	rev, e := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	if e != nil || !contains([]string{"verified", "rejected"}, st) || len(note) < 5 || len(note) > 1000 || (st == "verified" && r.FormValue("attest") != "on") {
		a.fail(w, r, "نتیجه، شرح بررسی و تأیید تطبیق هویت لازم است", 400)
		return
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	var uid string
	e = tx.QueryRow(ctx, "SELECT user_id FROM wallet_bank_accounts WHERE id=$1", r.PathValue("id")).Scan(&uid)
	if e != nil {
		a.fail(w, r, "حساب پیدا نشد", 404)
		return
	}
	_, e = walletLock(ctx, tx, uid)
	var holder, name string
	var actual int64
	if e == nil {
		e = tx.QueryRow(ctx, "SELECT holder,revision FROM wallet_bank_accounts WHERE id=$1 FOR UPDATE", r.PathValue("id")).Scan(&holder, &actual)
	}
	if e == nil {
		e = tx.QueryRow(ctx, "SELECT name FROM users WHERE id=$1 FOR SHARE", uid).Scan(&name)
	}
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	if rev != actual || (st == "verified" && bankName(holder) != bankName(name)) {
		a.fail(w, r, "حساب یا نام تغییر کرده؛ دوباره بررسی کنید", 409)
		return
	}
	_, e = tx.Exec(ctx, "UPDATE wallet_bank_accounts SET status=$2,reviewer_id=$3,review_note=$4,reviewed_at=now(),updated_at=now() WHERE id=$1", r.PathValue("id"), st, sess(r).User.ID, note)
	if e == nil {
		e = walletAudit(ctx, tx, sess(r).User.ID, "wallet_bank_review", r.PathValue("id"), map[string]any{"status": st, "revision": rev, "note": note})
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "بررسی ثبت نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/management/wallet#banks", "نتیجهٔ بررسی دستی ثبت شد."))
}
func (a *App) walletAdjust(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	n, e := walletAmount(r.FormValue("amount"))
	pool := r.FormValue("pool")
	direction := r.FormValue("direction")
	reason := strings.TrimSpace(r.FormValue("reason"))
	key := r.FormValue("request_key")
	uid := r.PathValue("id")
	if e != nil || !contains([]string{"cash", "bonus"}, pool) || !contains([]string{"credit", "debit"}, direction) || len(reason) < 5 || len(reason) > 1000 || len(key) != 64 {
		a.fail(w, r, "مبلغ، نوع، جهت و دلیل معتبر لازم است", 400)
		return
	}
	if direction == "debit" {
		n = -n
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	v, e := walletLock(ctx, tx, uid)
	if e != nil {
		a.fail(w, r, "کاربر پیدا نشد", 404)
		return
	}
	if v.Frozen || (pool == "cash" && v.Cash+n < 0) || (pool == "bonus" && v.Bonus+n < 0) {
		a.fail(w, r, "کیف مسدود است یا موجودی برای اصلاح کافی نیست", 409)
		return
	}
	cash, bonus := int64(0), int64(0)
	if pool == "cash" {
		cash = n
	} else {
		bonus = n
	}
	e = walletPost(ctx, tx, uid, "admin_"+pool, cash, bonus, 0, "adjust:"+key, "", sess(r).User.ID, reason)
	if e == nil {
		e = walletAudit(ctx, tx, sess(r).User.ID, "wallet_adjust", uid, map[string]any{"cash": cash, "bonus": bonus, "reason": reason, "key": key})
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "اصلاح ثبت نشد؛ شناسهٔ تکراری را بررسی کنید", 409)
		return
	}
	redirect(w, r, noticeURL("/management/wallet?user="+uid, "اصلاح با تراکنش جدید در دفتر ثبت شد؛ سابقه حذف یا ویرایش نمی‌شود."))
}
func (a *App) walletFreeze(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	reason := strings.TrimSpace(r.FormValue("reason"))
	if len(reason) < 5 || len(reason) > 1000 {
		a.fail(w, r, "دلیل لازم است", 400)
		return
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	uid := r.PathValue("id")
	_, e = walletLock(ctx, tx, uid)
	if e == nil {
		_, e = tx.Exec(ctx, "UPDATE wallets SET frozen=$2,updated_at=now() WHERE user_id=$1", uid, r.FormValue("frozen") == "on")
	}
	if e == nil {
		e = walletAudit(ctx, tx, sess(r).User.ID, "wallet_freeze", uid, map[string]any{"frozen": r.FormValue("frozen") == "on", "reason": reason})
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/management/wallet?user="+uid, "وضعیت کیف پول ثبت شد؛ اعتبارهای ورودی و تسویهٔ پرداختِ آغازشده حفظ می‌شوند."))
}
func (a *App) walletSettingsSave(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	mi, e1 := walletAmount(r.FormValue("min"))
	ma, e2 := walletAmount(r.FormValue("max"))
	da, e3 := strconv.ParseInt(normalize(r.FormValue("daily")), 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || ma < mi || da < ma || da > 10000000000 {
		a.fail(w, r, "حداقل، حداکثر و سقف روزانه معتبر وارد کنید", 400)
		return
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, "UPDATE wallet_settings SET min_withdrawal=$1,max_withdrawal=$2,daily_limit=$3,withdrawals_enabled=$4 WHERE id=1", mi, ma, da, r.FormValue("enabled") == "on")
	if e == nil {
		e = walletAudit(ctx, tx, sess(r).User.ID, "wallet_settings", "1", map[string]any{"min": mi, "max": ma, "daily": da, "enabled": r.FormValue("enabled") == "on"})
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "تنظیمات ثبت نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/management/wallet#settings", "محدودیت‌های برداشت ذخیره شدند."))
}
func (a *App) walletWithdrawalAction(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin", "ops") {
		return
	}
	action := r.FormValue("action")
	if action == "reject" {
		a.walletRelease(w, r, true)
		return
	}
	if !contains([]string{"claim", "pay", "reset"}, action) {
		a.fail(w, r, "اقدام نامعتبر", 400)
		return
	}
	var picture []byte
	var sender, account, digest string
	if action == "pay" {
		sender = strings.TrimSpace(r.FormValue("sender_name"))
		account = strings.TrimSpace(normalize(r.FormValue("sender_account")))
		if len(sender) < 3 || len(sender) > 200 || len(account) < 5 || len(account) > 100 {
			a.fail(w, r, "نام صاحب حساب و شماره حساب مبدأ را کامل کنید", 400)
			return
		}
		f, _, e := r.FormFile("receipt")
		if e != nil {
			a.fail(w, r, "تصویر رسید لازم است", 400)
			return
		}
		defer f.Close()
		raw, e := io.ReadAll(io.LimitReader(f, (8<<20)+1))
		if e != nil || len(raw) > 8<<20 {
			a.fail(w, r, "رسید حداکثر ۸ مگابایت باشد", 400)
			return
		}
		if e = validateProfileImage(raw); e != nil {
			a.fail(w, r, e.Error(), 400)
			return
		}
		im, _, e := image.Decode(bytes.NewReader(raw))
		if e != nil {
			a.fail(w, r, "تصویر معتبر لازم است", 400)
			return
		}
		var out bytes.Buffer
		if e = jpeg.Encode(&out, im, &jpeg.Options{Quality: 90}); e != nil {
			a.fail(w, r, "رسید ذخیره نشد", 400)
			return
		}
		picture = out.Bytes()
		if len(picture) > 8<<20 {
			a.fail(w, r, "تصویر بازنویسی‌شده بزرگ است؛ عکس را کوچک‌تر کنید", 400)
			return
		}
		h := sha256.Sum256(picture)
		digest = hex.EncodeToString(h[:])
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	var uid, st, operator, bankID string
	var amount, revision int64
	id := r.PathValue("id")
	e = tx.QueryRow(ctx, "SELECT user_id,status,amount,COALESCE(operator_id,''),bank_id,bank_revision FROM wallet_withdrawals WHERE id=$1 FOR UPDATE", id).Scan(&uid, &st, &amount, &operator, &bankID, &revision)
	if e != nil {
		a.fail(w, r, "درخواست پیدا نشد", 404)
		return
	}
	actor := sess(r).User.ID
	if action == "claim" {
		if st != "pending" {
			a.fail(w, r, "این درخواست قبلاً توسط اپراتور دیگری گرفته یا بسته شده است", 409)
			return
		}
		wallet, er := walletLock(ctx, tx, uid)
		if er != nil || wallet.Frozen {
			a.fail(w, r, "کیف پول مسدود یا در دسترس نیست", 409)
			return
		}
		var bankStatus, holder, name string
		var current int64
		e = tx.QueryRow(ctx, "SELECT status,revision,holder FROM wallet_bank_accounts WHERE id=$1 FOR SHARE", bankID).Scan(&bankStatus, &current, &holder)
		if e == nil {
			e = tx.QueryRow(ctx, "SELECT name FROM users WHERE id=$1 FOR SHARE", uid).Scan(&name)
		}
		if e != nil || bankStatus != "verified" || current != revision || bankName(holder) != bankName(name) {
			a.fail(w, r, "حساب بانکی یا نام تغییر کرده است؛ درخواست را رد و با شبای تأییدشده دوباره ثبت کنید", 409)
			return
		}
		_, e = tx.Exec(ctx, "UPDATE wallet_withdrawals SET status='processing',operator_id=$2,processing_at=now(),updated_at=now() WHERE id=$1", id, actor)
	} else if action == "pay" {
		if st != "processing" || operator != actor {
			a.fail(w, r, "فقط اپراتوری که پرداخت را آغاز کرده می‌تواند رسید آن را ثبت کند", 409)
			return
		}
		_, e = tx.Exec(ctx, "UPDATE wallet_withdrawals SET status='paid',sender_name=$2,sender_account=$3,receipt=$4,receipt_hash=$5,paid_at=now(),updated_at=now() WHERE id=$1", id, sender, account, picture, digest)
		if e == nil {
			e = walletPost(ctx, tx, uid, "withdrawal_paid", 0, 0, -amount, "paid:"+id, "", actor, "ثبت رسید برداشت "+id[:8])
		}
	} else {
		if sess(r).User.Role != "admin" || st != "processing" || r.FormValue("no_transfer") != "on" || len(strings.TrimSpace(r.FormValue("reason"))) < 5 || len(r.FormValue("reason")) > 1000 {
			a.fail(w, r, "فقط مدیر پس از تطبیق با بانک و تأیید عدم انتقال وجه می‌تواند پرداخت را به صف برگرداند", 409)
			return
		}
		_, e = tx.Exec(ctx, "UPDATE wallet_withdrawals SET status='pending',operator_id=NULL,processing_at=NULL,reason=$2,updated_at=now() WHERE id=$1", id, strings.TrimSpace(r.FormValue("reason")))
	}
	if e == nil {
		e = walletAudit(ctx, tx, actor, "wallet_withdrawal_"+action, id, map[string]any{"amount": amount, "receipt_sha256": digest, "reason": r.FormValue("reason")})
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "ثبت نشد؛ احتمال رسید تکراری یا خطای دیتابیس را بررسی کنید. تا تطبیق با بانک دوباره واریز نکنید", 409)
		return
	}
	redirect(w, r, noticeURL("/management/wallet", "اقدام ثبت شد؛ ثبت سایت به‌تنهایی تأیید بانک نیست. مبلغ و زمان در سابقه محفوظ‌اند."))
}
func (a *App) walletReceipt(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	var data []byte
	var uid string
	e := a.DB.QueryRow(r.Context(), "SELECT user_id,receipt FROM wallet_withdrawals WHERE id=$1 AND status='paid'", r.PathValue("id")).Scan(&uid, &data)
	u := sess(r).User
	if e != nil || (uid != u.ID && u.Role != "admin" && u.Role != "ops") {
		a.fail(w, r, "رسید پیدا نشد", 404)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="keepet-receipt-%s.jpg"`, r.PathValue("id")))
	w.Write(data)
}
