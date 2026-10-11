package app

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (a *App) ops(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "ops", "admin") {
		return
	}
	p := a.page(r, "مرکز عملیات")
	var err error
	p.Bookings, err = a.bookings(r.Context(), "payment_status='paid'")
	if err != nil {
		a.fail(w, r, "صف عملیات بارگذاری نشد", 500)
		return
	}
	p.Providers, _ = a.providers(r.Context(), "")
	rows, err := a.DB.Query(r.Context(), "SELECT id,user_id,service,name,license,bio,area,price,status,reason FROM applications ORDER BY created_at DESC")
	if err == nil {
		for rows.Next() {
			var ap Application
			rows.Scan(&ap.ID, &ap.UserID, &ap.Service, &ap.Name, &ap.License, &ap.Bio, &ap.Area, &ap.Price, &ap.Status, &ap.Reason)
			p.Applications = append(p.Applications, ap)
		}
		rows.Close()
	}
	p.Tickets, _ = a.tickets(r, "")
	a.render(w, r, "ops", p)
}
func validMeeting(s string) bool {
	if s == "" {
		return true
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}
func (a *App) opsAction(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "ops", "admin") {
		return
	}
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		a.fail(w, r, "خطای دیتابیس", 500)
		return
	}
	defer tx.Rollback(ctx)
	b, err := scanBooking(tx.QueryRow(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE id=$1 FOR UPDATE", r.PathValue("id")))
	if err != nil {
		a.fail(w, r, "رزرو پیدا نشد", 404)
		return
	}
	action := r.FormValue("action")
	detail := ""
	event := action
	switch action {
	case "confirm":
		if b.Status != "paid" {
			a.fail(w, r, "رزرو آمادهٔ تأیید نیست", 409)
			return
		}
		_, err = tx.Exec(ctx, "UPDATE bookings SET status='confirmed' WHERE id=$1", b.ID)
		detail = "درخواست توسط عملیات بررسی و تأیید شد."
	case "assign":
		if !contains([]string{"paid", "confirmed"}, b.Status) {
			a.fail(w, r, "ابتدا درخواست را تأیید کنید", 409)
			return
		}
		selected := r.FormValue("provider")
		if b.Service == "vet" && selected != b.ProviderID {
			a.fail(w, r, "تعویض دامپزشک نیازمند پذیرش مشتری است", 409)
			return
		}
		var active, verified bool
		var svc string
		var species, areas []string
		err = tx.QueryRow(ctx, "SELECT active,verified,service,species,areas FROM providers WHERE id=$1", selected).Scan(&active, &verified, &svc, &species, &areas)
		if err != nil || !active || !verified || svc != b.Service || !contains(species, b.Species) || (svc != "vet" && !contains(areas, b.Area)) {
			a.fail(w, r, "ارائه‌دهنده برای این درخواست صلاحیت یا پوشش ندارد", 400)
			return
		}
		exact, e := time.ParseInLocation("2006-01-02T15:04", r.FormValue("exact_time"), a.Location)
		meeting := r.FormValue("meeting_url")
		if e != nil || !exact.After(time.Now()) || !validMeeting(meeting) {
			a.fail(w, r, "زمان آینده و لینک معتبر HTTPS وارد کنید", 400)
			return
		}
		periodKey := ""
		h := exact.Hour()
		switch {
		case h >= 9 && h < 12:
			periodKey = "morning"
		case h >= 12 && h < 15:
			periodKey = "noon"
		case h >= 15 && h < 18:
			periodKey = "afternoon"
		case h >= 18 && h < 21:
			periodKey = "night"
		}
		var slot string
		var cap, used int
		err = tx.QueryRow(ctx, "SELECT id,capacity FROM availability WHERE provider_id=$1 AND day=$2 AND period=$3 FOR UPDATE", selected, exact.Format("2006-01-02"), periodKey).Scan(&slot, &cap)
		if err == nil {
			err = tx.QueryRow(ctx, "SELECT count(*) FROM bookings WHERE availability_id=$1 AND id!=$2 AND payment_status='paid' AND status NOT IN ('cancelled','cancel_requested')", slot, b.ID).Scan(&used)
		}
		if err != nil || used >= cap {
			a.fail(w, r, "برای این زمان ظرفیت ثبت نشده است", 409)
			return
		}
		_, err = tx.Exec(ctx, "UPDATE bookings SET provider_id=$2,availability_id=$3,exact_time=$4,meeting_url=$5,status='assigned' WHERE id=$1", b.ID, selected, slot, exact, meeting)
		detail = "زمان دقیق با مشتری و ارائه‌دهنده هماهنگ شد."
	case "replace":
		if b.Service != "vet" || !contains([]string{"paid", "confirmed", "assigned", "accepted"}, b.Status) {
			a.fail(w, r, "جایگزینی در این مرحله مجاز نیست", 409)
			return
		}
		id := r.FormValue("provider")
		var ok bool
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM providers WHERE id=$1 AND active AND verified AND service='vet' AND $2=ANY(species))", id, b.Species).Scan(&ok)
		if err != nil || !ok || id == b.ProviderID {
			a.fail(w, r, "دامپزشک جایگزین معتبر انتخاب کنید", 400)
			return
		}
		_, err = tx.Exec(ctx, "UPDATE bookings SET replacement_id=$2,status='replacement_pending' WHERE id=$1", b.ID, id)
		event = "replacement_pending"
		detail = "دامپزشک جایگزین پیشنهاد شد؛ مشتری می‌تواند بپذیرد یا با بازگشت کامل لغو کند."
	case "refund":
		if b.Status != "cancel_requested" {
			a.fail(w, r, "درخواست بازگشت وجود ندارد", 409)
			return
		}
		_, err = tx.Exec(ctx, "UPDATE bookings SET status='cancelled',refund_status='refunded' WHERE id=$1", b.ID)
		event = "cancelled"
		detail = "بازگشت آزمایشی " + money(b.RefundAmount) + " تومان ثبت شد؛ جابه‌جایی واقعی وجه انجام نشده است."
	case "platform_cancel":
		if contains([]string{"draft", "cancelled", "completed", "result_ready", "cancel_requested"}, b.Status) {
			a.fail(w, r, "این رزرو قابل لغو از این مسیر نیست", 409)
			return
		}
		reason := strings.TrimSpace(r.FormValue("notes"))
		if reason == "" {
			a.fail(w, r, "دلیل لغو و پیشنهاد جبران را ثبت کنید", 400)
			return
		}
		_, err = tx.Exec(ctx, "UPDATE bookings SET status='cancel_requested',refund_amount=amount,refund_status='pending_refund',refund_reason=$2 WHERE id=$1", b.ID, "کیپت: "+reason)
		event = "cancel_requested"
		detail = "لغو از سمت کیپت؛ بازگشت کامل و بررسی جبران. " + reason
	case "result":
		if b.Service != "sampling" || b.Status != "delivered" || strings.TrimSpace(r.FormValue("result")) == "" {
			a.fail(w, r, "نتیجه پس از تحویل به آزمایشگاه قابل ثبت است", 409)
			return
		}
		_, err = tx.Exec(ctx, "UPDATE bookings SET result=$2,status='result_ready' WHERE id=$1", b.ID, r.FormValue("result"))
		event = "result_ready"
		detail = "نتیجهٔ آزمایش در حساب مشتری قرار گرفت."
	default:
		a.fail(w, r, "اقدام نامعتبر", 400)
		return
	}
	if err == nil {
		err = addEvent(ctx, tx, b.ID, sess(r).User.ID, event, detail)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		a.fail(w, r, "تغییر وضعیت ثبت نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/ops", "تغییر وضعیت ثبت شد."))
}
func (a *App) addAvailability(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "ops", "admin") {
		return
	}
	day, err := time.ParseInLocation("2006-01-02", r.FormValue("day"), a.Location)
	cap, e := strconv.Atoi(r.FormValue("capacity"))
	prd := r.FormValue("period")
	if err != nil || e != nil || cap < 1 || cap > 20 || !contains([]string{"morning", "noon", "afternoon", "night"}, prd) || day.Before(time.Now().In(a.Location).Truncate(24*time.Hour).Add(-24*time.Hour)) {
		a.fail(w, r, "تاریخ، ظرفیت و بازهٔ معتبر وارد کنید", 400)
		return
	}
	id := r.FormValue("provider")
	_, err = a.DB.Exec(r.Context(), "INSERT INTO availability(id,provider_id,day,period,capacity) VALUES($1,$2,$3,$4,$5) ON CONFLICT(provider_id,day,period) DO UPDATE SET capacity=excluded.capacity", token(), id, day.Format("2006-01-02"), prd, cap)
	if err != nil {
		a.fail(w, r, "ثبت بازه انجام نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/ops", "بازهٔ در دسترس ثبت شد."))
}
func (a *App) providerPanel(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "provider", "owner") {
		return
	}
	p := a.page(r, "پنل ارائه‌دهنده")
	var providerID string
	err := a.DB.QueryRow(r.Context(), "SELECT id FROM providers WHERE user_id=$1 AND active", p.User.ID).Scan(&providerID)
	if err == nil {
		p.Bookings, _ = a.bookings(r.Context(), "provider_id=$1 AND status IN ('assigned','accepted','in_progress','collected','delivered','completed','result_ready')", providerID)
	}
	rows, err := a.DB.Query(r.Context(), "SELECT id,user_id,service,name,license,bio,area,price,status,reason FROM applications WHERE user_id=$1 ORDER BY created_at DESC", p.User.ID)
	if err == nil {
		for rows.Next() {
			var ap Application
			rows.Scan(&ap.ID, &ap.UserID, &ap.Service, &ap.Name, &ap.License, &ap.Bio, &ap.Area, &ap.Price, &ap.Status, &ap.Reason)
			p.Applications = append(p.Applications, ap)
		}
		rows.Close()
	}
	a.render(w, r, "provider", p)
}
func (a *App) providerAction(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "provider") {
		return
	}
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		a.fail(w, r, "خطای دیتابیس", 500)
		return
	}
	defer tx.Rollback(ctx)
	b, err := scanBooking(tx.QueryRow(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE id=$1 FOR UPDATE", r.PathValue("id")))
	var userID string
	if err == nil {
		err = tx.QueryRow(ctx, "SELECT user_id FROM providers WHERE id=$1", b.ProviderID).Scan(&userID)
	}
	if err != nil || userID != sess(r).User.ID {
		a.fail(w, r, "به این کار دسترسی ندارید", 404)
		return
	}
	action := r.FormValue("action")
	next, detail := "", ""
	switch action {
	case "accept":
		if b.Status == "assigned" {
			next = "accepted"
			detail = "ارائه‌دهنده کار را پذیرفت."
		}
	case "reject":
		if b.Status == "assigned" {
			next = "paid"
			detail = "ارائه‌دهنده کار را رد کرد؛ برای هماهنگی مجدد به صف عملیات بازگشت."
		}
	case "start":
		if b.Status == "accepted" {
			next = "in_progress"
			detail = "خدمت شروع شد."
		}
	case "collected":
		if b.Service == "sampling" && b.Status == "in_progress" {
			next = "collected"
			detail = "نمونهٔ ساده گرفته شد."
		}
	case "delivered":
		if b.Service == "sampling" && b.Status == "collected" {
			next = "delivered"
			detail = "نمونه به آزمایشگاه تحویل شد."
		}
	case "complete":
		if b.Status == "in_progress" && b.Service != "sampling" {
			if b.Service == "vet" && strings.TrimSpace(r.FormValue("prescription")) == "" {
				a.fail(w, r, "خلاصهٔ مشاوره و نسخه را ثبت کنید", 400)
				return
			}
			next = "completed"
			detail = "خدمت تکمیل شد."
		}
	}
	if next == "" {
		a.fail(w, r, "ترتیب مراحل رعایت نشده است", 409)
		return
	}
	_, err = tx.Exec(ctx, "UPDATE bookings SET status=$2,prescription=CASE WHEN $3='' THEN prescription ELSE $3 END WHERE id=$1", b.ID, next, r.FormValue("prescription"))
	if next == "paid" && err == nil {
		_, err = tx.Exec(ctx, "UPDATE bookings SET exact_time=NULL,meeting_url='' WHERE id=$1", b.ID)
	}
	if err == nil {
		err = addEvent(ctx, tx, b.ID, sess(r).User.ID, next, detail)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		a.fail(w, r, "تغییر ثبت نشد", 500)
		return
	}
	redirect(w, r, "/provider")
}
func (a *App) join(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "join", a.page(r, "همکاری با کیپت"))
}
func (a *App) apply(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	svc := r.FormValue("service")
	name := strings.TrimSpace(r.FormValue("name"))
	bio := strings.TrimSpace(r.FormValue("bio"))
	price, err := strconv.ParseInt(normalize(r.FormValue("price")), 10, 64)
	if service(svc).ID == "" || name == "" || bio == "" || err != nil || price < 0 || (svc == "vet" && r.FormValue("license") == "") {
		a.fail(w, r, "اطلاعات، تعرفه و مدارک صلاحیت را کامل کنید", 400)
		return
	}
	_, err = a.DB.Exec(r.Context(), "INSERT INTO applications(id,user_id,service,name,license,bio,area,price) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", token(), sess(r).User.ID, svc, name, r.FormValue("license"), bio, r.FormValue("area"), price)
	if err != nil {
		a.fail(w, r, "درخواست ثبت نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/provider", "درخواست همکاری ثبت شد؛ پس از بررسی عملیات فعال می‌شود."))
}
func (a *App) applicationDecision(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "ops", "admin") {
		return
	}
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		a.fail(w, r, "خطای دیتابیس", 500)
		return
	}
	defer tx.Rollback(ctx)
	var ap Application
	err = tx.QueryRow(ctx, "SELECT id,user_id,service,name,license,bio,area,price,status FROM applications WHERE id=$1 FOR UPDATE", r.PathValue("id")).Scan(&ap.ID, &ap.UserID, &ap.Service, &ap.Name, &ap.License, &ap.Bio, &ap.Area, &ap.Price, &ap.Status)
	if err != nil || ap.Status != "pending" {
		a.fail(w, r, "درخواست قابل بررسی نیست", 409)
		return
	}
	st := r.FormValue("decision")
	reason := r.FormValue("reason")
	if st == "approved" {
		if r.FormValue("verified") != "on" || ap.Service == "vet" && ap.License == "" {
			a.fail(w, r, "بررسی صلاحیت و مجوز را تأیید کنید", 400)
			return
		}
		areas := []string{}
		if areaName(ap.Area) != "خارج از پوشش" {
			areas = append(areas, ap.Area)
		}
		_, err = tx.Exec(ctx, "INSERT INTO providers(id,user_id,name,service,bio,license,verified,active,areas,price) VALUES($1,$2,$3,$4,$5,$6,true,true,$7,$8) ON CONFLICT(user_id) DO UPDATE SET name=excluded.name,bio=excluded.bio,license=excluded.license,service=excluded.service,areas=excluded.areas,price=excluded.price,verified=true,active=true", ap.ID, ap.UserID, ap.Name, ap.Service, ap.Bio, ap.License, areas, ap.Price)
		if err == nil {
			_, err = tx.Exec(ctx, "UPDATE users SET role='provider' WHERE id=$1 AND role='owner'", ap.UserID)
		}
	} else if st != "rejected" || strings.TrimSpace(reason) == "" {
		a.fail(w, r, "دلیل رد درخواست لازم است", 400)
		return
	}
	if err == nil {
		_, err = tx.Exec(ctx, "UPDATE applications SET status=$2,reason=$3 WHERE id=$1", ap.ID, st, reason)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		a.fail(w, r, "بررسی ذخیره نشد", 500)
		return
	}
	redirect(w, r, "/ops")
}
func (a *App) admin(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	p := a.page(r, "مدیریت کسب‌وکار")
	a.DB.QueryRow(r.Context(), "SELECT count(*),COALESCE(sum(amount),0) FROM bookings WHERE payment_status='paid'").Scan(&p.Count, &p.Total)
	p.Bookings, _ = a.bookings(r.Context(), "payment_status='paid'")
	p.Providers, _ = a.providers(r.Context(), "")
	a.render(w, r, "admin", p)
}
