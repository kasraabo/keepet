package app

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (a *App) ops(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "ops", "admin") {
		return
	}
	p := a.page(r, "مرکز عملیات")
	var err error
	p.Bookings, err = a.bookings(r.Context(), "payment_status='paid' OR status='pending_ops'")
	if err != nil {
		a.fail(w, r, "صف عملیات بارگذاری نشد", 500)
		return
	}
	p.Providers, _ = a.providers(r.Context(), "")
	p.AssignmentSlots = make(map[string][]Slot)
	for _, b := range p.Bookings {
		if b.Status == "paid" || b.Status == "confirmed" {
			p.AssignmentSlots[b.ID], err = a.assignmentSlots(r.Context(), b)
			if err != nil {
				a.fail(w, r, "بازه‌های قابل تخصیص بارگذاری نشدند", 500)
				return
			}
		}
	}
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
	p.Chats, err = a.chatSummaries(r, "")
	if err != nil {
		a.fail(w, r, "گفتگوها بارگذاری نشد", 500)
		return
	}
	for i := range p.Applications {
		p.Applications[i].Form = a.savedForm(r.Context(), "signup-"+p.Applications[i].Service, p.Applications[i].ID)
	}
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
	if b.RequestFlow && b.PaymentStatus != "paid" {
		a.fail(w, r, "برای درخواست پرداخت‌نشده از کنترل تأیید درخواست استفاده کنید", 409)
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
		if !validMeeting(r.FormValue("meeting_url")) {
			a.fail(w, r, "لینک ارتباط باید HTTPS معتبر باشد", 400)
			return
		}
		slot, exact, msg, code := a.assignedWindow(ctx, tx, b, r)
		if code != 0 {
			a.fail(w, r, msg, code)
			return
		}
		_, err = tx.Exec(ctx, "UPDATE bookings SET provider_id=$2,availability_id=$3,exact_time=$4,meeting_url=$5,status='assigned' WHERE id=$1", b.ID, slot.ProviderID, slot.ID, exact, r.FormValue("meeting_url"))
		detail = "زمان دقیق با مشتری و ارائه‌دهنده هماهنگ شد."
	case "replace":
		if b.RequestFlow {
			a.fail(w, r, "برای تغییر متخصص، سفارش را با بازگشت کامل لغو و درخواست تازه ثبت کنید", 409)
			return
		}
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
		detail = "عودت " + money(b.RefundAmount) + " تومان به کیف پول ثبت شد؛ بخش هدیه به اعتبار هدیه برگشت و انتقال بانکی انجام نشده است."
	case "platform_cancel":
		if b.FulfillmentFlow && b.SettlementApprovedAt != nil {
			a.fail(w, r, "این خدمت تسویه شده است؛ برای بازگشت وجه بررسی مالی لازم است", 409)
			return
		}
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
		if b.LabFlow {
			a.fail(w, r, "نتیجه را از صفحهٔ نتیجهٔ آزمایش ثبت و منتشر کنید", 409)
			return
		}
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
	notice := "تغییر وضعیت ثبت شد."
	if action == "confirm" {
		notice = "بررسی سفارش تأیید شد. برای نمایش در کارهای متخصص، بخش «تخصیص و ثبت زمان دقیق» همین سفارش را تکمیل کنید."
	} else if action == "assign" {
		notice = "سفارش به متخصص تخصیص داده شد و در پنل «کارهای من» او قابل پذیرش است."
	}
	redirect(w, r, noticeURL(opsHome(r), notice))
}
func (a *App) addAvailability(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "ops", "admin") {
		return
	}
	a.saveAvailability(w, r, r.FormValue("provider"), opsHome(r))
}
func (a *App) providerAvailability(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "provider") {
		return
	}
	var id string
	if a.DB.QueryRow(r.Context(), "SELECT id FROM providers WHERE user_id=$1 AND active AND verified", sess(r).User.ID).Scan(&id) != nil {
		a.fail(w, r, "حساب متخصص باید فعال و تأییدشده باشد", 403)
		return
	}
	a.saveAvailability(w, r, id, "/provider")
}
func (a *App) providerPanel(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "provider", "owner") {
		return
	}
	p := a.page(r, "پنل ارائه‌دهنده")
	var providerID string
	err := a.DB.QueryRow(r.Context(), "SELECT id FROM providers WHERE user_id=$1 AND active", p.User.ID).Scan(&providerID)
	if err == nil {
		p.Next = providerID
		p.Profile, _ = a.specialist(r.Context(), providerID)
		slotRows, slotErr := a.DB.Query(r.Context(), `SELECT a.id,a.day::text,a.period,to_char(a.start_time,'HH24:MI'),to_char(a.end_time,'HH24:MI'),a.capacity,booking_window_peak(a.id,'') FROM availability a WHERE a.provider_id=$1 AND a.day>=(now() AT TIME ZONE 'Asia/Tehran')::date ORDER BY a.day,a.start_time LIMIT 360`, providerID)
		if slotErr != nil {
			a.fail(w, r, "بازه‌ها بارگذاری نشدند", 500)
			return
		}
		for slotRows.Next() {
			var slot Slot
			slotErr = slotRows.Scan(&slot.ID, &slot.Day, &slot.Period, &slot.StartTime, &slot.EndTime, &slot.Capacity, &slot.Used)
			if slotErr != nil {
				break
			}
			a.slotTimes(&slot)
			p.Slots = append(p.Slots, slot)
		}
		if slotErr == nil {
			slotErr = slotRows.Err()
		}
		slotRows.Close()
		if slotErr != nil {
			a.fail(w, r, "بازه‌ها بارگذاری نشدند", 500)
			return
		}
		p.Bookings, err = a.bookings(r.Context(), "provider_id=$1 AND status IN ('pending_provider','awaiting_payment','assigned','accepted','in_progress','awaiting_customer','delivery_pending','awaiting_report','collected','delivered','completed','result_ready')", providerID)
		if err != nil {
			a.fail(w, r, "سفارش‌ها بارگذاری نشدند؛ صفحه را تازه کنید", 500)
			return
		}
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
	var qualified bool
	a.DB.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM providers WHERE user_id=$1 AND service='vet' AND active AND verified)", p.User.ID).Scan(&qualified)
	if qualified {
		rs, _ := a.records(r.Context(), "article")
		for _, v := range rs {
			if v.Data.ReviewedBy == "" {
				p.Posts = append(p.Posts, v)
			}
		}
	}
	p.Chats, err = a.chatSummaries(r, "")
	if err != nil {
		a.fail(w, r, "پروندهٔ گفتگو بارگذاری نشد", 500)
		return
	}
	for i := range p.Applications {
		p.Applications[i].Form = a.savedForm(r.Context(), "signup-"+p.Applications[i].Service, p.Applications[i].ID)
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
	if b.FulfillmentFlow && b.Service != "vet" && b.PaymentStatus == "paid" {
		a.fail(w, r, "برای حضور و انجام خدمت، از صفحهٔ پیگیری همین سفارش استفاده کنید", 409)
		return
	}
	action := r.FormValue("action")
	chatID := ""
	if b.Service == "vet" && action == "complete" {
		// Clinical completion belongs to the participant questionnaire; the old
		// prescription-only action cannot bypass it.
		a.fail(w, r, "پایان ویزیت و ثبت گزارش پزشکی را از داخل گفتگو انجام دهید", 409)
		return
	}
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
			if b.Service == "vet" {
				chatID, err = a.beginChat(ctx, tx, b)
				if err != nil {
					a.fail(w, r, "شروع گفتگو ممکن نیست؛ صلاحیت و پرداخت را بررسی کنید", 409)
					return
				}
			}
			next = "in_progress"
			detail = "خدمت شروع شد."
		}
	case "start_chat":
		if b.Service == "vet" && b.Status == "in_progress" {
			chatID, err = a.beginChat(ctx, tx, b)
			if err != nil {
				a.fail(w, r, "گفتگو قابل شروع نیست", 409)
				return
			}
			next = "in_progress"
			detail = "گفتگوی داخلی برای ویزیت قبلی فعال شد."
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
			next = "completed"
			detail = "خدمت تکمیل شد."
		}
	}
	if next == "" {
		a.fail(w, r, "ترتیب مراحل رعایت نشده است", 409)
		return
	}
	_, err = tx.Exec(ctx, "UPDATE bookings SET status=$2 WHERE id=$1", b.ID, next)
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
	if chatID != "" {
		redirect(w, r, "/chats/"+chatID)
		return
	}
	redirect(w, r, "/provider")
}
func (a *App) join(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "همکاری با کیپت")
	for _, s := range []string{"vet", "grooming", "sampling"} {
		p.Forms = append(p.Forms, a.form(r.Context(), "signup-"+s, map[string]string{"name": func() string {
			if p.User != nil {
				return p.User.Name
			}
			return ""
		}(), "price": "350000"}))
	}
	a.render(w, r, "join", p)
}
func (a *App) apply(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "owner", "provider") {
		return
	}
	svc := r.FormValue("service")
	name := strings.TrimSpace(r.FormValue("name"))
	license := strings.TrimSpace(r.FormValue("license"))
	if svc != "vet" {
		license = ""
	}
	bio := strings.TrimSpace(r.FormValue("bio"))
	price, err := strconv.ParseInt(normalizeNumber(r.FormValue("price")), 10, 64)
	if service(svc).ID == "" || name == "" || bio == "" || err != nil || price < 0 || (svc == "vet" && license == "") {
		a.fail(w, r, "اطلاعات، تعرفه و مدارک صلاحیت را کامل کنید", 400)
		return
	}
	f := a.form(r.Context(), "signup-"+svc, nil)
	answers, e := formAnswers(r, f)
	if e != nil {
		a.fail(w, r, e.Error(), 400)
		return
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(r.Context())
	id := token()
	if svc != "vet" {
		answers["license"] = ""
	}
	_, err = tx.Exec(r.Context(), "UPDATE users SET name=$2 WHERE id=$1 AND role IN ('owner','provider')", sess(r).User.ID, name)
	if err != nil {
		a.fail(w, r, "نام ذخیره نشد", 500)
		return
	}
	_, err = tx.Exec(r.Context(), "INSERT INTO applications(id,user_id,service,name,license,bio,area,price,documents_required) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)", id, sess(r).User.ID, svc, name, license, bio, r.FormValue("area"), price, svc == "vet")
	if err == nil {
		err = saveFormResponse(r.Context(), tx, f, id, sess(r).User.ID, answers)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.fail(w, r, "درخواست ثبت نشد", 500)
		return
	}
	next := "/provider"
	if svc == "vet" {
		next = "/applications/" + id + "/documents"
	}
	msg := "درخواست همکاری ثبت شد؛ پس از بررسی عملیات فعال می‌شود."
	if svc == "vet" {
		msg = "درخواست همکاری ثبت شد؛ مدارک دامپزشکی را بارگذاری کنید. فعال‌سازی پس از بررسی عملیات است."
	}
	redirect(w, r, noticeURL(next, msg))
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
		var ready bool
		if e := tx.QueryRow(ctx, "SELECT NOT documents_required OR EXISTS(SELECT 1 FROM application_documents WHERE application_id=applications.id) FROM applications WHERE id=$1", ap.ID).Scan(&ready); e != nil || !ready {
			a.fail(w, r, "برای تأیید درخواست دامپزشکی، ابتدا مدارک بارگذاری‌شده را بررسی کنید", 409)
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
	redirect(w, r, opsHome(r))
}

func opsHome(r *http.Request) string {
	if strings.HasPrefix(r.URL.Path, "/management/") {
		return "/management/operations"
	}
	return "/ops"
}
