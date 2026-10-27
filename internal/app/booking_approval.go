package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type ApprovalSettings struct {
	Revision                         int64
	Ops, Provider, Payment, Reminder int
}
type ApprovalProvider struct {
	ID, Name, Service string
	Required          bool
	Revision          int64
}
type ApprovalView struct {
	Settings  ApprovalSettings
	Providers []ApprovalProvider
}

func (a *App) approvalSettingsPage(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	p := a.page(r, "تنظیم تأیید و مهلت سفارش")
	v := &ApprovalView{}
	p.Approval = v
	err := a.DB.QueryRow(r.Context(), "SELECT revision,ops_minutes,provider_minutes,payment_minutes,reminder_minutes FROM booking_approval_settings WHERE id=1").Scan(&v.Settings.Revision, &v.Settings.Ops, &v.Settings.Provider, &v.Settings.Payment, &v.Settings.Reminder)
	if err != nil {
		a.fail(w, r, "تنظیمات بارگذاری نشد", 500)
		return
	}
	rows, err := a.DB.Query(r.Context(), "SELECT id,name,service,requires_ops_approval,approval_revision FROM providers ORDER BY name")
	if err != nil {
		a.fail(w, r, "متخصصان بارگذاری نشدند", 500)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var x ApprovalProvider
		if err = rows.Scan(&x.ID, &x.Name, &x.Service, &x.Required, &x.Revision); err != nil {
			break
		}
		v.Providers = append(v.Providers, x)
	}
	if err != nil || rows.Err() != nil {
		a.fail(w, r, "متخصصان بارگذاری نشدند", 500)
		return
	}
	a.render(w, r, "approval-settings", p)
}
func (a *App) saveApprovalSettings(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	vals := []int{}
	for _, key := range []string{"ops_minutes", "provider_minutes", "payment_minutes", "reminder_minutes"} {
		v, e := strconv.Atoi(normalize(r.FormValue(key)))
		if e != nil || v < 1 || v > 1440 {
			a.fail(w, r, "مدت باید بین یک تا ۱۴۴۰ دقیقه باشد", 400)
			return
		}
		vals = append(vals, v)
	}
	rev, e := strconv.ParseInt(normalize(r.FormValue("revision")), 10, 64)
	if e != nil {
		a.fail(w, r, "نسخهٔ فرم معتبر نیست", 400)
		return
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(r.Context())
	var before []byte
	e = tx.QueryRow(r.Context(), "SELECT to_jsonb(s) FROM booking_approval_settings s WHERE id=1 AND revision=$1 FOR UPDATE", rev).Scan(&before)
	if e != nil {
		tx.Rollback(r.Context())
		a.fail(w, r, "تنظیمات تغییر کرده؛ صفحه را تازه کنید", 409)
		return
	}
	_, e = tx.Exec(r.Context(), "UPDATE booking_approval_settings SET ops_minutes=$1,provider_minutes=$2,payment_minutes=$3,reminder_minutes=$4,revision=revision+1 WHERE id=1", vals[0], vals[1], vals[2], vals[3])
	if e == nil {
		_, e = tx.Exec(r.Context(), "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,'booking_approval_settings','1',$2,$3)", sess(r).User.ID, before, mustJSON(map[string]any{"ops_minutes": vals[0], "provider_minutes": vals[1], "payment_minutes": vals[2], "reminder_minutes": vals[3], "revision": rev + 1}))
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		tx.Rollback(r.Context())
		a.fail(w, r, "ثبت تنظیمات انجام نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/management/booking-policy", "تنظیمات برای درخواست‌های جدید ذخیره شد."))
}
func (a *App) saveProviderApproval(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	rev, e := strconv.ParseInt(normalize(r.FormValue("revision")), 10, 64)
	if e != nil {
		a.fail(w, r, "نسخهٔ فرم معتبر نیست", 400)
		return
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(r.Context())
	var before []byte
	e = tx.QueryRow(r.Context(), "SELECT jsonb_build_object('requires_ops_approval',requires_ops_approval,'revision',approval_revision) FROM providers WHERE id=$1 AND approval_revision=$2 FOR UPDATE", r.PathValue("id"), rev).Scan(&before)
	if e != nil {
		tx.Rollback(r.Context())
		a.fail(w, r, "اطلاعات تغییر کرده؛ صفحه را تازه کنید", 409)
		return
	}
	required := r.FormValue("required") == "on"
	_, e = tx.Exec(r.Context(), "UPDATE providers SET requires_ops_approval=$2,approval_revision=approval_revision+1 WHERE id=$1", r.PathValue("id"), required)
	if e == nil {
		_, e = tx.Exec(r.Context(), "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,'provider_approval',$2,$3,$4)", sess(r).User.ID, r.PathValue("id"), before, mustJSON(map[string]any{"requires_ops_approval": required, "revision": rev + 1}))
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		tx.Rollback(r.Context())
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/management/booking-policy", "مسیر درخواست‌های جدید این متخصص ذخیره شد."))
}
func requestEvent(ctx context.Context, tx pgx.Tx, b Booking, actor, event, detail string) error {
	if _, e := tx.Exec(ctx, "INSERT INTO booking_events(booking_id,actor_id,event,detail) VALUES($1,NULLIF($2,''),$3,$4)", b.ID, actor, event, detail); e != nil {
		return e
	}
	// Outbox remains local until a real delivery adapter is configured.
	var recipient string
	if event == "pending_provider" {
		if e := tx.QueryRow(ctx, "SELECT COALESCE(user_id,'') FROM providers WHERE id=$1", b.ProviderID).Scan(&recipient); e != nil {
			return e
		}
	} else {
		recipient = b.OwnerID
	}
	if recipient != "" {
		_, e := tx.Exec(ctx, "INSERT INTO outbox(booking_id,user_id,kind,payload,dedup_key,status) VALUES($1,$2,$3,$4,$5,'local')", b.ID, recipient, event, mustJSON(map[string]string{"detail": detail, "delivery": "not_sent", "service": b.Service}), "request-notice:"+b.ID+":"+event)
		return e
	}
	return nil
}
func (a *App) submitRequest(w http.ResponseWriter, r *http.Request) {
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
	if e != nil || b.OwnerID != sess(r).User.ID {
		fail("درخواست پیدا نشد", 404)
		return
	}
	if b.RequestFlow && b.Status != "draft" {
		redirect(w, r, "/bookings/"+b.ID)
		return
	}
	if !b.RequestFlow || b.Stage != 4 || b.PaymentKey != r.FormValue("payment_key") {
		fail("درخواست آمادهٔ ثبت نیست", 409)
		return
	}
	locks, lockErr := tx.Query(ctx, "SELECT id FROM business_config WHERE kind IN ('service','area','grooming','sampling') ORDER BY kind,id FOR SHARE")
	if lockErr != nil {
		fail("تنظیمات درخواست در دسترس نیست", 503)
		return
	}
	for locks.Next() {
	}
	lockErr = locks.Err()
	locks.Close()
	if lockErr != nil {
		fail("تنظیمات درخواست در دسترس نیست", 503)
		return
	}
	if b.LabFlow {
		if _, err := tx.Exec(ctx, "SELECT id FROM lab_catalog WHERE id=1 FOR SHARE"); err != nil {
			fail("کاتالوگ در دسترس نیست", 503)
			return
		}
	}
	var policy []byte
	var need bool
	var active bool
	e = tx.QueryRow(ctx, "SELECT to_jsonb(s) FROM booking_approval_settings s WHERE id=1 FOR SHARE").Scan(&policy)
	if e == nil {
		e = tx.QueryRow(ctx, "SELECT requires_ops_approval,active AND verified FROM providers WHERE id=$1 FOR SHARE", b.ProviderID).Scan(&need, &active)
	}
	if e != nil || !active {
		fail("متخصص در دسترس نیست", 409)
		return
	}
	slots, err := a.slots(ctx, b)
	valid := false
	for _, s := range slots {
		if s.ID == b.AvailabilityID && (!b.TimeFlow || (b.ExactTime != nil && s.Start.Equal(*b.ExactTime))) {
			valid = true
		}
	}
	q, err2 := a.quote(ctx, b)
	var raw []byte
	e = tx.QueryRow(ctx, "SELECT pricing_snapshot FROM bookings WHERE id=$1", b.ID).Scan(&raw)
	if err != nil || err2 != nil || !valid || e != nil || string(raw) == "null" {
		fail("زمان یا تعرفه تغییر کرده؛ انتخاب را دوباره بررسی کنید", 409)
		return
	}
	var saved PriceQuote
	if json.Unmarshal(raw, &saved) != nil || string(mustJSON(saved)) != string(mustJSON(q)) {
		fail("تعرفه تغییر کرده؛ بازه و خدمات را دوباره بررسی کنید", 409)
		return
	}
	state := "pending_provider"
	minutes := 0
	var config map[string]int
	if json.Unmarshal(policy, &config) != nil {
		fail("تنظیمات معتبر نیست", 500)
		return
	}
	minutes = config["provider_minutes"]
	if need {
		state = "pending_ops"
		minutes = config["ops_minutes"]
	}
	_, e = tx.Exec(ctx, "UPDATE bookings SET status=$2,completion_hours=(SELECT completion_hours FROM fulfillment_settings WHERE id=1),submitted_at=now(),requires_ops_approval=$3,approval_policy=$4,stage_deadline=CASE WHEN time_flow THEN LEAST(exact_time,now()+$5*interval '1 minute') ELSE now()+$5*interval '1 minute' END,provider_requested_at=CASE WHEN $2='pending_provider' THEN now() ELSE NULL END,provider_due=$6,commission_bps=COALESCE((SELECT (data->>'commission')::integer FROM business_config WHERE kind='service' AND id=bookings.service),0) WHERE id=$1", b.ID, state, need, policy, minutes, q.Base)
	if e == nil {
		e = requestEvent(ctx, tx, b, sess(r).User.ID, state, "درخواست ثبت شد؛ پرداخت پس از تأیید متخصص انجام می‌شود.")
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		fail("ثبت درخواست انجام نشد", 500)
		return
	}
	redirect(w, r, "/bookings/"+b.ID)
}
func (a *App) requestAction(w http.ResponseWriter, r *http.Request) {
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
	if e != nil || !b.RequestFlow {
		fail("درخواست پیدا نشد", 404)
		return
	}
	actor := sess(r).User
	var providerUser string
	tx.QueryRow(ctx, "SELECT COALESCE(user_id,'') FROM providers WHERE id=$1", b.ProviderID).Scan(&providerUser)
	action := r.FormValue("action")
	ops := actor.Role == "ops" || actor.Role == "admin"
	own := actor.ID == b.OwnerID
	specialist := actor.ID == providerUser
	if !ops && !own && !specialist {
		fail("به درخواست دسترسی ندارید", 403)
		return
	}
	if action == "retry" && own && contains([]string{"request_rejected", "request_expired", "request_cancelled"}, b.Status) {
		if b.FollowupID != "" {
			var current string
			if e = tx.QueryRow(ctx, "SELECT COALESCE(booking_id,'') FROM care_followups WHERE id=$1 FOR UPDATE", b.FollowupID).Scan(&current); e != nil {
				fail("پیگیری در دسترس نیست", 409)
				return
			}
			if current != "" && current != b.ID {
				var state string
				tx.QueryRow(ctx, "SELECT status FROM bookings WHERE id=$1", current).Scan(&state)
				redirect(w, r, careBookingURL(current, state))
				return
			}
		}

		id := token()
		_, e = tx.Exec(ctx, `INSERT INTO bookings(id,session_token,owner_id,service,stage,pet_name,species,age,weight,answers,area,address,items,sample,consent,payment_key,request_flow,time_flow,fulfillment_flow,lab_flow,followup_id) SELECT $2,$3,owner_id,service,2,pet_name,species,age,weight,answers,area,address,items,sample,consent,$4,true,true,true,service='sampling',followup_id FROM bookings WHERE id=$1`, b.ID, id, sess(r).Token, token())
		if e == nil && b.FollowupID != "" {
			_, e = tx.Exec(ctx, "UPDATE care_followups SET booking_id=$2,updated_at=now() WHERE id=$1", b.FollowupID, id)
		}
		if e == nil {
			e = tx.Commit(ctx)
		}
		if e != nil {
			fail("ادامهٔ انتخاب ثبت نشد", 500)
			return
		}
		redirect(w, r, "/booking/"+id)
		return
	}
	if !contains([]string{"pending_ops", "pending_provider", "awaiting_payment"}, b.Status) {
		fail("این درخواست دیگر منتظر تأیید نیست", 409)
		return
	}
	if action == "accept" && specialist && b.Status == "awaiting_payment" {
		fail("این درخواست قبلاً پذیرفته شده است", 409)
		return
	}
	if b.StageDeadline == nil || !b.StageDeadline.After(time.Now()) {
		fail("مهلت این مرحله پایان یافته است", 409)
		return
	}
	var policy map[string]int
	var raw []byte
	e = tx.QueryRow(ctx, "SELECT approval_policy FROM bookings WHERE id=$1", b.ID).Scan(&raw)
	if e != nil || json.Unmarshal(raw, &policy) != nil {
		fail("شرایط درخواست خوانده نشد", 500)
		return
	}
	next, reason := "", ""
	minutes := 0
	switch {
	case action == "cancel" && own:
		next = "request_cancelled"
		reason = "درخواست پیش از پرداخت توسط مشتری لغو شد."
	case action == "reject" && ((ops && b.Status == "pending_ops") || (specialist && b.Status == "pending_provider")):
		next = "request_rejected"
		reason = "متأسفیم، امکان پذیرش این درخواست فراهم نشد. می‌توانید متخصص دیگری انتخاب کنید."
	case action == "approve" && ops && b.Status == "pending_ops":
		next = "pending_provider"
		minutes = policy["provider_minutes"]
		reason = "اپراتور درخواست را تأیید کرد؛ اکنون منتظر پاسخ متخصص است."
	case action == "accept" && specialist && b.Status == "pending_provider":
		var eligible bool
		if err := tx.QueryRow(ctx, "SELECT active AND verified AND service=$2 AND $3=ANY(species) AND (service='vet' OR $4=ANY(areas)) FROM providers WHERE id=$1 FOR SHARE", b.ProviderID, b.Service, b.Species, b.Area).Scan(&eligible); err != nil || !eligible || !a.enabled(ctx, "service", b.Service) || (b.Service != "vet" && !a.enabled(ctx, "area", b.Area)) {
			fail("متخصص یا محدودهٔ خدمت در دسترس نیست", 409)
			return
		}
		if _, er := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,13013))", b.ProviderID); er != nil {
			fail("بررسی ظرفیت انجام نشد", 500)
			return
		}
		var capacity, used int
		var end time.Time
		e = tx.QueryRow(ctx, "SELECT capacity,(day+end_time) AT TIME ZONE 'Asia/Tehran' FROM availability WHERE id=$1 FOR UPDATE", b.AvailabilityID).Scan(&capacity, &end)
		if e == nil {
			e = tx.QueryRow(ctx, "SELECT count(*) FROM bookings WHERE availability_id=$1 AND id<>$2 AND booking_holds_capacity(payment_status,status,stage_deadline)", b.AvailabilityID, b.ID).Scan(&used)
		}
		if b.TimeFlow && e == nil {
			var free bool
			if b.ExactTime == nil {
				fail("ساعت انتخاب نشده است", 409)
				return
			}
			e = tx.QueryRow(ctx, "SELECT booking_time_available($1,$2,$3,$4,$5)", b.ProviderID, b.AvailabilityID, b.ExactTime, b.DurationMinutes, b.ID).Scan(&free)
			if !free {
				used = capacity
			} else {
				used = 0
			}
		}
		if !b.TimeFlow && e == nil {
			var free bool
			e = tx.QueryRow(ctx, `SELECT booking_time_available($1,$2,GREATEST((day+start_time) AT TIME ZONE 'Asia/Tehran',date_trunc('minute',now())+interval '1 minute'),(extract(epoch FROM (((day+end_time) AT TIME ZONE 'Asia/Tehran')-GREATEST((day+start_time) AT TIME ZONE 'Asia/Tehran',date_trunc('minute',now())+interval '1 minute')))/60)::integer,$3) FROM availability WHERE id=$2`, b.ProviderID, b.AvailabilityID, b.ID).Scan(&free)
			if !free {
				used = capacity
			} else {
				used = 0
			}
		}
		if e != nil || used >= capacity || !end.After(time.Now()) {
			fail("زمان انتخابی دیگر ظرفیت ندارد؛ درخواست قابل پذیرش نیست", 409)
			return
		}
		next = "awaiting_payment"
		minutes = policy["payment_minutes"]
		reason = "متخصص درخواست را پذیرفت؛ برای پرداخت و قطعی‌کردن رزرو اقدام کنید."
	default:
		fail("این اقدام برای نقش یا مرحلهٔ شما مجاز نیست", 403)
		return
	}
	_, e = tx.Exec(ctx, "UPDATE bookings SET status=$2,stage_deadline=CASE WHEN $3>0 THEN CASE WHEN time_flow THEN LEAST(exact_time,now()+$3*interval '1 minute') ELSE now()+$3*interval '1 minute' END ELSE NULL END,provider_requested_at=CASE WHEN $2='pending_provider' THEN now() ELSE provider_requested_at END,request_reason=$4 WHERE id=$1", b.ID, next, minutes, reason)
	if e == nil {
		e = requestEvent(ctx, tx, b, actor.ID, next, reason)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		fail("تغییر ثبت نشد", 500)
		return
	}
	home := "/bookings/" + b.ID
	if ops {
		home = opsHome(r)
	} else if specialist {
		home = "/provider"
	}
	redirect(w, r, noticeURL(home, "وضعیت درخواست ثبت شد."))
}
func (a *App) reconcileRequests(ctx context.Context) error {
	rows, e := a.DB.Query(ctx, `SELECT id FROM bookings WHERE request_flow AND status IN ('pending_ops','pending_provider','awaiting_payment') AND (stage_deadline<=now() OR (status='pending_provider' AND extract(epoch FROM now()-provider_requested_at)>=60*(approval_policy->>'reminder_minutes')::integer AND NOT EXISTS(SELECT 1 FROM outbox o WHERE o.dedup_key='request-reminder:'||bookings.id||':'||floor(extract(epoch FROM now()-provider_requested_at)/60/(approval_policy->>'reminder_minutes')::integer)::integer::text))) ORDER BY stage_deadline LIMIT 200`)
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
		if e = a.reconcileRequest(ctx, id); e != nil {
			return e
		}
	}
	return nil
}
func (a *App) reconcileRequest(ctx context.Context, id string) error {
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	b, e := scanBooking(tx.QueryRow(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE id=$1 FOR UPDATE SKIP LOCKED", id))
	if e == pgx.ErrNoRows {
		return nil
	}
	if e != nil {
		return e
	}
	if !contains([]string{"pending_ops", "pending_provider", "awaiting_payment"}, b.Status) {
		return nil
	}
	if b.StageDeadline != nil && !b.StageDeadline.After(time.Now()) {
		reason := "مهلت این مرحله پایان یافت؛ می‌توانید با اطلاعات قبلی دوباره انتخاب کنید."
		_, e = tx.Exec(ctx, "UPDATE bookings SET status='request_expired',request_reason=$2 WHERE id=$1", id, reason)
		if e == nil {
			e = requestEvent(ctx, tx, b, "", "request_expired", reason)
		}
	} else if b.Status == "pending_provider" {
		var minutes int
		var started time.Time
		var uid string
		e = tx.QueryRow(ctx, "SELECT (approval_policy->>'reminder_minutes')::integer,provider_requested_at,COALESCE((SELECT user_id FROM providers WHERE id=bookings.provider_id),'') FROM bookings WHERE id=$1", id).Scan(&minutes, &started, &uid)
		if e == nil && minutes > 0 && uid != "" {
			elapsed := int(time.Since(started).Minutes())
			n := elapsed / minutes
			if n > 0 {
				_, e = tx.Exec(ctx, "INSERT INTO outbox(booking_id,user_id,kind,payload,dedup_key,status) VALUES($1,$2,'provider_response_reminder',$3,$4,'local') ON CONFLICT(dedup_key) DO NOTHING", id, uid, mustJSON(map[string]string{"delivery": "not_sent", "detail": "درخواست منتظر پاسخ شماست."}), fmt.Sprintf("request-reminder:%s:%d", id, n))
			}
		}
	}
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
