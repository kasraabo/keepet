package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math/big"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type FulfillmentSettings struct {
	Revision                                 int64
	Minutes, Attempts, Resend, Hourly, Hours int
}
type HandoverView struct {
	ID, Lab, Reference, State, Reason, Filename string
	At                                          time.Time
}
type FulfillmentView struct {
	Settings                                     FulfillmentSettings
	Code, Issue                                  string
	Expires, ReportedAt, Deadline                *time.Time
	Owner, Provider, Ops, Disputed, HasChallenge bool
	Receipt                                      *HandoverView
}

func (a *App) fulfillmentConfig(ctx context.Context) (FulfillmentSettings, error) {
	var s FulfillmentSettings
	e := a.DB.QueryRow(ctx, "SELECT revision,arrival_minutes,arrival_attempts,resend_seconds,hourly_limit,completion_hours FROM fulfillment_settings WHERE id=1").Scan(&s.Revision, &s.Minutes, &s.Attempts, &s.Resend, &s.Hourly, &s.Hours)
	return s, e
}
func (a *App) fulfillmentSettingsPage(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	s, e := a.fulfillmentConfig(r.Context())
	if e != nil {
		a.fail(w, r, "تنظیمات بارگذاری نشد", 500)
		return
	}
	p := a.page(r, "تنظیم حضور و پایان خدمت")
	p.Fulfillment = &FulfillmentView{Settings: s}
	a.render(w, r, "fulfillment-settings", p)
}
func (a *App) saveFulfillmentSettings(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	keys := []string{"arrival_minutes", "arrival_attempts", "resend_seconds", "hourly_limit", "completion_hours"}
	mins := []int{1, 1, 15, 1, 1}
	maxs := []int{60, 10, 600, 20, 168}
	vs := make([]int, 5)
	for i, k := range keys {
		v, e := strconv.Atoi(normalize(r.FormValue(k)))
		if e != nil || v < mins[i] || v > maxs[i] {
			a.fail(w, r, "مقدار تنظیم خارج از محدودهٔ مجاز است", 400)
			return
		}
		vs[i] = v
	}
	revision, e := strconv.ParseInt(normalize(r.FormValue("revision")), 10, 64)
	if e != nil {
		a.fail(w, r, "نسخه معتبر نیست", 400)
		return
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(r.Context())
	var before []byte
	e = tx.QueryRow(r.Context(), "SELECT to_jsonb(s) FROM fulfillment_settings s WHERE id=1 AND revision=$1 FOR UPDATE", revision).Scan(&before)
	if e != nil {
		tx.Rollback(r.Context())
		a.fail(w, r, "تنظیمات تغییر کرده؛ صفحه را تازه کنید", 409)
		return
	}
	_, e = tx.Exec(r.Context(), "UPDATE fulfillment_settings SET revision=revision+1,arrival_minutes=$1,arrival_attempts=$2,resend_seconds=$3,hourly_limit=$4,completion_hours=$5 WHERE id=1", vs[0], vs[1], vs[2], vs[3], vs[4])
	if e == nil {
		after := map[string]int64{"revision": revision + 1}
		for i, k := range keys {
			after[k] = int64(vs[i])
		}
		_, e = tx.Exec(r.Context(), "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,'fulfillment_settings','1',$2,$3)", sess(r).User.ID, before, mustJSON(after))
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		tx.Rollback(r.Context())
		a.fail(w, r, "ثبت تنظیمات انجام نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/management/fulfillment", "تنظیمات ذخیره شد؛ مهلت پایان خدمتِ درخواست‌های قبلی تغییر نمی‌کند."))
}
func (a *App) fulfillmentPage(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	b, e := a.getBooking(r)
	if e != nil || !b.FulfillmentFlow || !contains([]string{"grooming", "sampling"}, b.Service) {
		a.fail(w, r, "صفحهٔ خدمت پیدا نشد", 404)
		return
	}
	u := sess(r).User
	var provider string
	a.DB.QueryRow(r.Context(), "SELECT COALESCE(user_id,'') FROM providers WHERE id=$1", b.ProviderID).Scan(&provider)
	v := &FulfillmentView{Owner: u.ID == b.OwnerID, Provider: u.ID == provider, Ops: contains([]string{"admin", "ops"}, u.Role)}
	if !v.Owner && !v.Provider && !v.Ops {
		a.fail(w, r, "به این سفارش دسترسی ندارید", 403)
		return
	}
	p := a.page(r, "حضور و پیگیری خدمت")
	p.Booking = &b
	p.Fulfillment = v
	if b.LabFlow {
		var raw []byte
		if e := a.DB.QueryRow(r.Context(), "SELECT pricing_snapshot FROM bookings WHERE id=$1", b.ID).Scan(&raw); e == nil {
			var q PriceQuote
			if json.Unmarshal(raw, &q) == nil {
				p.Pricing = &q
			}
		}
	}
	e = a.DB.QueryRow(r.Context(), "SELECT completion_reported_at,completion_deadline,fulfillment_disputed,fulfillment_issue FROM bookings WHERE id=$1", b.ID).Scan(&v.ReportedAt, &v.Deadline, &v.Disputed, &v.Issue)
	if e != nil {
		a.fail(w, r, "اطلاعات خدمت بارگذاری نشد", 500)
		return
	}
	var raw []byte
	var expires time.Time
	var used *time.Time
	var attempts, maxAttempts int
	e = a.DB.QueryRow(r.Context(), "SELECT code_cipher,expires_at,consumed_at,attempts,max_attempts FROM arrival_challenges WHERE booking_id=$1", b.ID).Scan(&raw, &expires, &used, &attempts, &maxAttempts)
	if e == nil {
		v.HasChallenge = true
		v.Expires = &expires
		if v.Owner && b.PaymentStatus == "paid" && b.RefundStatus == "" && used == nil && b.AttendanceVerifiedAt == nil && expires.After(time.Now()) && attempts < maxAttempts {
			v.Code, e = openIntegration("arrival:"+b.ID, raw)
			if e != nil {
				a.fail(w, r, "کد حضور در دسترس نیست؛ از متخصص بخواهید کد تازه درخواست کند", 503)
				return
			}
		}
	} else if e != pgx.ErrNoRows {
		a.fail(w, r, "وضعیت کد حضور بارگذاری نشد", 500)
		return
	}
	receipt := HandoverView{}
	e = a.DB.QueryRow(r.Context(), "SELECT r.id,r.lab_name,r.reference,r.state,r.reason,r.filename,r.created_at FROM handover_receipts r JOIN bookings b ON b.handover_receipt_id=r.id WHERE b.id=$1", b.ID).Scan(&receipt.ID, &receipt.Lab, &receipt.Reference, &receipt.State, &receipt.Reason, &receipt.Filename, &receipt.At)
	if e == nil {
		v.Receipt = &receipt
	} else if e != pgx.ErrNoRows {
		a.fail(w, r, "رسید بارگذاری نشد", 500)
		return
	}
	a.render(w, r, "fulfillment", p)
}
func (a *App) fulfillmentAction(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	fail := func(msg string, code int) { tx.Rollback(ctx); a.fail(w, r, msg, code) }
	b, e := scanBooking(tx.QueryRow(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE id=$1 FOR UPDATE", r.PathValue("id")))
	if e != nil || !b.FulfillmentFlow || !contains([]string{"grooming", "sampling"}, b.Service) {
		fail("سفارش پیدا نشد", 404)
		return
	}
	actor := sess(r).User
	var provider string
	var eligible bool
	e = tx.QueryRow(ctx, "SELECT COALESCE(user_id,''),active AND verified FROM providers WHERE id=$1", b.ProviderID).Scan(&provider, &eligible)
	if e != nil {
		fail("متخصص پیدا نشد", 404)
		return
	}
	own := actor.ID == b.OwnerID
	pro := actor.ID == provider && actor.Role == "provider" && eligible
	ops := contains([]string{"admin", "ops"}, actor.Role)
	if !own && !pro && !ops {
		fail("به این سفارش دسترسی ندارید", 403)
		return
	}
	if b.PaymentStatus != "paid" || b.RefundStatus != "" || contains([]string{"cancelled", "cancel_requested", "replacement_pending"}, b.Status) {
		fail("این سفارش برای انجام خدمت فعال نیست", 409)
		return
	}
	if b.SettlementApprovedAt != nil {
		redirect(w, r, noticeURL("/bookings/"+b.ID+"/fulfillment", "این خدمت قبلاً نهایی و ثبت شده است."))
		return
	}
	var disputed bool
	var currentReceipt string
	e = tx.QueryRow(ctx, "SELECT fulfillment_disputed,COALESCE(handover_receipt_id,'') FROM bookings WHERE id=$1", b.ID).Scan(&disputed, &currentReceipt)
	if e != nil {
		fail("وضعیت خدمت خوانده نشد", 500)
		return
	}
	action := r.FormValue("action")
	detail, event := "", action
	switch action {
	case "arrived":
		if !pro {
			fail("فقط متخصص همان سفارش می‌تواند رسیدن را اعلام کند", 403)
			return
		}
		if b.Status != "accepted" || b.AttendanceVerifiedAt != nil || disputed {
			fail("این سفارش آمادهٔ ثبت حضور نیست", 409)
			return
		}
		s, err := a.fulfillmentConfig(ctx)
		if err != nil {
			fail("تنظیمات کد در دسترس نیست", 500)
			return
		}
		var last, hour time.Time
		var count int
		var oldChallenge, oldHash string
		err = tx.QueryRow(ctx, "SELECT last_sent_at,hour_started_at,send_count,challenge_id,code_hash FROM arrival_challenges WHERE booking_id=$1 FOR UPDATE", b.ID).Scan(&last, &hour, &count, &oldChallenge, &oldHash)
		if err != nil && err != pgx.ErrNoRows {
			fail("کد قابل ثبت نیست", 500)
			return
		}
		now := time.Now()
		if err == nil && now.Sub(last) < time.Duration(s.Resend)*time.Second {
			fail("برای درخواست مجدد کد کمی صبر کنید", 429)
			return
		}
		if err == nil && now.Sub(hour) < time.Hour && count >= s.Hourly {
			fail("تعداد درخواست کد زیاد است؛ بعداً تلاش کنید", 429)
			return
		}
		if err != nil || now.Sub(hour) >= time.Hour {
			hour = now
			count = 0
		}
		n, err := rand.Int(rand.Reader, big.NewInt(1000000))
		if err != nil {
			fail("ساخت کد انجام نشد", 500)
			return
		}
		code := fmt.Sprintf("%06d", n.Int64())
		// A resend must not accidentally issue the same visible code again.
		for oldHash != "" && hash(b.ID+oldChallenge+code) == oldHash {
			n, err = rand.Int(rand.Reader, big.NewInt(1000000))
			if err != nil {
				fail("ساخت کد انجام نشد", 500)
				return
			}
			code = fmt.Sprintf("%06d", n.Int64())
		}
		challenge := token()
		sealed, err := sealIntegration("arrival:"+b.ID, code)
		if err != nil {
			fail("کلید امن کد حضور در دسترس نیست", 503)
			return
		}
		_, e = tx.Exec(ctx, `INSERT INTO arrival_challenges(booking_id,challenge_id,code_hash,code_cipher,expires_at,max_attempts,hour_started_at,send_count) VALUES($1,$2,$3,$4,now()+$5*interval '1 minute',$6,$7,$8) ON CONFLICT(booking_id) DO UPDATE SET challenge_id=EXCLUDED.challenge_id,code_hash=EXCLUDED.code_hash,code_cipher=EXCLUDED.code_cipher,expires_at=EXCLUDED.expires_at,max_attempts=EXCLUDED.max_attempts,attempts=0,consumed_at=NULL,last_sent_at=now(),hour_started_at=EXCLUDED.hour_started_at,send_count=EXCLUDED.send_count`, b.ID, challenge, hash(b.ID+challenge+code), sealed, s.Minutes, s.Attempts, hour, count+1)
		if e == nil {
			_, e = tx.Exec(ctx, "INSERT INTO outbox(booking_id,user_id,kind,payload,dedup_key,status) VALUES($1,$2,'arrival_code',$3,$4,'local')", b.ID, b.OwnerID, mustJSON(map[string]string{"challenge": challenge, "delivery": "not_sent"}), "arrival:"+challenge)
		}
		detail = "متخصص اعلام رسیدن کرد؛ کد حضور در حساب مشتری ایجاد شد. حضور هنوز تأیید نشده است."
		event = "arrival_requested"
	case "verify_arrival":
		if !pro {
			fail("تأیید حضور فقط برای متخصص سفارش است", 403)
			return
		}
		if b.AttendanceVerifiedAt != nil {
			redirect(w, r, "/bookings/"+b.ID+"/fulfillment")
			return
		}
		if b.Status != "accepted" || disputed {
			fail("ثبت حضور در این مرحله مجاز نیست", 409)
			return
		}
		var challenge, stored string
		var expires time.Time
		var used *time.Time
		var attempts, max int
		e = tx.QueryRow(ctx, "SELECT challenge_id,code_hash,expires_at,consumed_at,attempts,max_attempts FROM arrival_challenges WHERE booking_id=$1 FOR UPDATE", b.ID).Scan(&challenge, &stored, &expires, &used, &attempts, &max)
		if e != nil || used != nil || !expires.After(time.Now()) || attempts >= max {
			fail("کد منقضی، مصرف‌شده یا مسدود است؛ کد تازه درخواست کنید", 409)
			return
		}
		code := normalize(strings.TrimSpace(r.FormValue("code")))
		if len(code) != 6 || subtle.ConstantTimeCompare([]byte(hash(b.ID+challenge+code)), []byte(stored)) != 1 {
			_, e = tx.Exec(ctx, "UPDATE arrival_challenges SET attempts=attempts+1 WHERE booking_id=$1", b.ID)
			if e == nil {
				e = tx.Commit(ctx)
			}
			if e != nil {
				fail("ثبت تلاش انجام نشد", 500)
				return
			}
			a.fail(w, r, "کد حضور صحیح نیست", 400)
			return
		}
		_, e = tx.Exec(ctx, "UPDATE arrival_challenges SET consumed_at=now() WHERE booking_id=$1", b.ID)
		if e == nil {
			_, e = tx.Exec(ctx, "UPDATE bookings SET attendance_verified_at=(SELECT consumed_at FROM arrival_challenges WHERE booking_id=$1),status='in_progress' WHERE id=$1", b.ID)
		}
		event = "attendance_verified"
		detail = "حضور متخصص با کد صحیح همان سفارش تأیید و زمان آن ثبت شد."
	case "groom_done":
		if !pro || b.Service != "grooming" {
			fail("این اقدام برای شما مجاز نیست", 403)
			return
		}
		if b.Status != "in_progress" || b.AttendanceVerifiedAt == nil || disputed {
			fail("ابتدا حضور باید تأیید و موانع سفارش بررسی شود", 409)
			return
		}
		_, e = tx.Exec(ctx, "UPDATE bookings SET completion_reported_at=now(),completion_deadline=now()+completion_hours*interval '1 hour',status='awaiting_customer' WHERE id=$1", b.ID)
		event = "service_reported_done"
		detail = "گرومر انجام خدمت را اعلام کرد؛ منتظر تأیید یا گزارش مشکل مشتری است."
	case "confirm_done":
		if !own || b.Service != "grooming" {
			fail("تأیید پایان خدمت فقط با مشتری همان سفارش است", 403)
			return
		}
		if b.Status != "awaiting_customer" || disputed {
			fail("پایان خدمت آمادهٔ تأیید نیست یا اعتراض باز دارد", 409)
			return
		}
		_, e = tx.Exec(ctx, "UPDATE bookings SET completion_method='customer',completion_confirmed_by=$2,settlement_approved_at=now(),status='completed' WHERE id=$1", b.ID, actor.ID)
		event = "completed"
		detail = "مشتری پایان گرومینگ را تأیید کرد؛ درآمد متخصص و سهم معرف ثبت شدند."
	case "collected":
		if !pro || b.Service != "sampling" {
			fail("این اقدام برای شما مجاز نیست", 403)
			return
		}
		if b.Status != "in_progress" || disputed || b.AttendanceVerifiedAt == nil {
			fail("ابتدا حضور باید تأیید شود", 409)
			return
		}
		_, e = tx.Exec(ctx, "UPDATE bookings SET status='collected' WHERE id=$1", b.ID)
		detail = "نمونه گرفته شد؛ تحویل به آزمایشگاه و بارگذاری رسید لازم است."
	case "upload_receipt":
		if !pro || b.Service != "sampling" {
			fail("بارگذاری رسید فقط برای نمونه‌بردار همان سفارش است", 403)
			return
		}
		if b.Status != "collected" || disputed || b.AttendanceVerifiedAt == nil {
			fail("ابتدا حضور و انجام نمونه‌گیری را ثبت کنید", 409)
			return
		}
		lab := strings.TrimSpace(r.FormValue("lab_name"))
		if b.LabFlow {
			var raw []byte
			var q PriceQuote
			if e := tx.QueryRow(ctx, "SELECT pricing_snapshot FROM bookings WHERE id=$1", b.ID).Scan(&raw); e != nil || json.Unmarshal(raw, &q) != nil || q.Lab == nil {
				fail("آزمایشگاه توافق‌شده معتبر نیست", 409)
				return
			}
			lab = q.Lab.LabName
		}
		reference := strings.TrimSpace(r.FormValue("reference"))
		if len(lab) < 3 || len(lab) > 300 || len(reference) > 200 {
			fail("نام آزمایشگاه و شمارهٔ رسید معتبر وارد کنید", 400)
			return
		}
		data, mimeType, filename, err := readHandover(r)
		if err != nil {
			fail(err.Error(), 400)
			return
		}
		id := token()
		digest := sha256.Sum256(data)
		_, e = tx.Exec(ctx, "INSERT INTO handover_receipts(id,booking_id,submitted_by,lab_name,reference,filename,mime,data,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)", id, b.ID, actor.ID, lab, reference, filename, mimeType, data, hex.EncodeToString(digest[:]))
		if e == nil {
			_, e = tx.Exec(ctx, "UPDATE bookings SET handover_receipt_id=$2,status='delivery_pending' WHERE id=$1", b.ID, id)
		}
		event = "handover_uploaded"
		detail = "رسید تحویل نمونه بارگذاری شد؛ تا تأیید اپراتور، درآمد تسویه نمی‌شود."
	case "approve_receipt", "reject_receipt":
		if !ops || b.Service != "sampling" {
			fail("بررسی رسید با اپراتور مجاز است", 403)
			return
		}
		if b.Status != "delivery_pending" || currentReceipt == "" || currentReceipt != r.FormValue("receipt_id") || disputed {
			fail("رسید آمادهٔ بررسی نیست یا نسخهٔ فرم قدیمی است", 409)
			return
		}
		var state string
		e = tx.QueryRow(ctx, "SELECT state FROM handover_receipts WHERE id=$1 AND booking_id=$2 FOR UPDATE", currentReceipt, b.ID).Scan(&state)
		if e != nil || state != "pending" {
			fail("این رسید قبلاً بررسی شده است", 409)
			return
		}
		next := "approved"
		reason := strings.TrimSpace(r.FormValue("reason"))
		if len(reason) > 3000 {
			fail("توضیح کوتاه‌تر وارد کنید", 400)
			return
		}
		if action == "reject_receipt" {
			next = "rejected"
			if len(reason) < 5 {
				fail("دلیل رد رسید لازم است", 400)
				return
			}
		}
		_, e = tx.Exec(ctx, "UPDATE handover_receipts SET state=$2,reason=$3,reviewed_by=$4,reviewed_at=now() WHERE id=$1", currentReceipt, next, reason, actor.ID)
		if e == nil && next == "approved" {
			_, e = tx.Exec(ctx, "UPDATE bookings SET receipt_approved_at=now(),receipt_approved_by=$2,settlement_approved_at=now(),status='delivered' WHERE id=$1", b.ID, actor.ID)
			event = "handover_approved"
			detail = "اپراتور رسید تحویل را تأیید کرد؛ درآمد نمونه‌بردار و سهم معرف ثبت شدند. نتیجهٔ آزمایش جداگانه منتشر می‌شود."
		} else if e == nil {
			_, e = tx.Exec(ctx, "UPDATE bookings SET status='collected' WHERE id=$1", b.ID)
			event = "handover_rejected"
			detail = "رسید تحویل رد شد؛ نمونه‌بردار باید رسید اصلاح‌شده بارگذاری کند. " + reason
		}
	case "report_issue":
		if !own {
			fail("گزارش مشکل فقط با مشتری سفارش است", 403)
			return
		}
		if !contains([]string{"accepted", "in_progress", "awaiting_customer", "collected", "delivery_pending"}, b.Status) {
			fail("برای بررسی این مرحله با پشتیبانی تماس بگیرید", 409)
			return
		}
		reason := strings.TrimSpace(r.FormValue("reason"))
		if len(reason) < 5 || len(reason) > 3000 {
			fail("شرح مشکل را کامل و کوتاه بنویسید", 400)
			return
		}
		if disputed {
			fail("گزارش قبلی در حال بررسی است", 409)
			return
		}
		_, e = tx.Exec(ctx, "UPDATE bookings SET fulfillment_disputed=true,fulfillment_issue=$2 WHERE id=$1", b.ID, reason)
		event = "service_issue"
		detail = "مشتری مشکل یا عدم حضور را گزارش کرد؛ نهایی‌سازی و تسویه تا بررسی اپراتور متوقف است. " + reason
	case "resolve_continue", "resolve_refund":
		if !ops {
			fail("رسیدگی به مشکل با اپراتور مجاز است", 403)
			return
		}
		reason := strings.TrimSpace(r.FormValue("reason"))
		if !disputed || len(reason) < 5 || len(reason) > 3000 {
			fail("گزارش باز و دلیل تصمیم اپراتور لازم است", 409)
			return
		}
		if action == "resolve_refund" {
			_, e = tx.Exec(ctx, "UPDATE bookings SET status='cancel_requested',refund_amount=amount,refund_status='pending_refund',refund_reason=$2 WHERE id=$1", b.ID, "بررسی خدمت: "+reason)
			event = "cancel_requested"
			detail = "اپراتور بازگشت کامل وجه را برای انجام از مسیر مالی ثبت کرد. " + reason
		} else {
			_, e = tx.Exec(ctx, "UPDATE bookings SET fulfillment_disputed=false,completion_deadline=CASE WHEN status='awaiting_customer' THEN now()+completion_hours*interval '1 hour' ELSE completion_deadline END WHERE id=$1", b.ID)
			event = "service_issue_resolved"
			detail = "اپراتور مانع ادامه را رفع کرد؛ برای پایان گرومینگ فرصت تازهٔ بررسی به مشتری داده شد. " + reason
		}
	default:
		fail("اقدام نامعتبر", 400)
		return
	}
	if e == nil {
		e = fulfillmentEvent(ctx, tx, b, actor.ID, event, detail)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		fail("ثبت مرحله انجام نشد؛ وضعیت سفارش را دوباره بررسی کنید", 409)
		return
	}
	redirect(w, r, noticeURL("/bookings/"+b.ID+"/fulfillment", "وضعیت خدمت ثبت شد."))
}
func readHandover(r *http.Request) ([]byte, string, string, error) {
	if r.MultipartForm == nil || len(r.MultipartForm.File["receipt"]) != 1 {
		return nil, "", "", fmt.Errorf("یک فایل رسید بارگذاری کنید")
	}
	fh := r.MultipartForm.File["receipt"][0]
	if fh.Size > 8<<20 {
		return nil, "", "", fmt.Errorf("حجم رسید حداکثر ۸ مگابایت باشد")
	}
	f, e := fh.Open()
	if e != nil {
		return nil, "", "", fmt.Errorf("فایل قابل خواندن نیست")
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if e != nil || len(raw) == 0 || len(raw) > 8<<20 {
		return nil, "", "", fmt.Errorf("فایل خالی یا بیش از حد مجاز است")
	}
	if bytes.HasPrefix(raw, []byte("%PDF-")) && bytes.Contains(raw[max(0, len(raw)-2048):], []byte("%%EOF")) {
		return raw, "application/pdf", "handover.pdf", nil
	}
	c, kind, e := image.DecodeConfig(bytes.NewReader(raw))
	if e != nil || !contains([]string{"jpeg", "png", "webp"}, kind) || c.Width < 1 || c.Height < 1 || int64(c.Width)*int64(c.Height) > 20_000_000 {
		return nil, "", "", fmt.Errorf("رسید باید تصویر معتبر JPEG، PNG، WebP یا PDF باشد")
	}
	im, _, e := image.Decode(bytes.NewReader(raw))
	if e != nil {
		return nil, "", "", fmt.Errorf("تصویر معتبر نیست")
	}
	var out bytes.Buffer
	if e = jpeg.Encode(&out, im, &jpeg.Options{Quality: 90}); e != nil || out.Len() > 8<<20 {
		return nil, "", "", fmt.Errorf("تصویر رسید را کوچک‌تر کنید")
	}
	return out.Bytes(), "image/jpeg", "handover.jpg", nil
}
func (a *App) handoverDownload(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	var data []byte
	var mimeType, filename, provider string
	e := a.DB.QueryRow(r.Context(), "SELECT h.data,h.mime,h.filename,COALESCE(p.user_id,'') FROM handover_receipts h JOIN bookings b ON b.id=h.booking_id JOIN providers p ON p.id=b.provider_id WHERE b.id=$1 AND h.id=$2", r.PathValue("id"), r.PathValue("receipt")).Scan(&data, &mimeType, &filename, &provider)
	if e != nil {
		a.fail(w, r, "رسید پیدا نشد", 404)
		return
	}
	u := sess(r).User
	if u.ID != provider && !contains([]string{"admin", "ops"}, u.Role) {
		a.fail(w, r, "به رسید دسترسی ندارید", 403)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Write(data)
}
func (a *App) reconcileFulfillments(ctx context.Context) error {
	rows, e := a.DB.Query(ctx, "SELECT id FROM bookings WHERE fulfillment_flow AND service='grooming' AND status='awaiting_customer' AND NOT fulfillment_disputed AND completion_deadline<=now() AND payment_status='paid' AND refund_status='' ORDER BY completion_deadline LIMIT 100")
	if e != nil {
		return e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			break
		}
		ids = append(ids, id)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range ids {
		tx, err := a.DB.Begin(ctx)
		if err != nil {
			return err
		}
		b, err := scanBooking(tx.QueryRow(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE id=$1 FOR UPDATE SKIP LOCKED", id))
		if err == pgx.ErrNoRows {
			tx.Rollback(ctx)
			continue
		}
		if err == nil {
			var done bool
			err = tx.QueryRow(ctx, "SELECT status='awaiting_customer' AND NOT fulfillment_disputed AND completion_deadline<=now() AND attendance_verified_at IS NOT NULL AND payment_status='paid' AND refund_status='' FROM bookings WHERE id=$1", id).Scan(&done)
			if err == nil && done {
				_, err = tx.Exec(ctx, "UPDATE bookings SET completion_method='automatic',settlement_approved_at=now(),status='completed' WHERE id=$1", id)
				if err == nil {
					err = fulfillmentEvent(ctx, tx, b, "", "completed", "مهلت بررسی پایان گرومینگ بدون اعتراض پایان یافت؛ خدمت و درآمدها نهایی شدند.")
				}
			}
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		tx.Rollback(ctx)
		if err != nil {
			return err
		}
	}
	return nil
}

// Booking row locks and state transitions make each financial action idempotent.
// Event IDs remain unique because a code resend or corrected receipt is a new event.
func fulfillmentEvent(ctx context.Context, tx pgx.Tx, b Booking, actor, event, detail string) error {
	if _, e := tx.Exec(ctx, "INSERT INTO booking_events(booking_id,actor_id,event,detail) VALUES($1,NULLIF($2,''),$3,$4)", b.ID, actor, event, detail); e != nil {
		return e
	}
	_, e := tx.Exec(ctx, "INSERT INTO outbox(booking_id,user_id,kind,payload,dedup_key,status) VALUES($1,$2,$3,$4,$5,'local')", b.ID, b.OwnerID, event, mustJSON(map[string]string{"detail": detail, "delivery": "not_sent", "service": b.Service}), "fulfillment:"+token())
	return e
}
