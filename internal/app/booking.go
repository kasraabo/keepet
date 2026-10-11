package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) createBooking(w http.ResponseWriter, r *http.Request) {
	svc := service(r.PathValue("service"))
	if svc.ID == "" || !a.enabled(r.Context(), "service", svc.ID) {
		a.fail(w, r, "خدمت نامعتبر", 404)
		return
	}
	id := token()
	owner := ""
	if sess(r).User != nil {
		owner = sess(r).User.ID
	}
	_, err := a.DB.Exec(r.Context(), "INSERT INTO bookings(id,session_token,owner_id,service,payment_key) VALUES($1,$2,NULLIF($3,''),$4,$5)", id, sess(r).Token, owner, svc.ID, token())
	if err != nil {
		a.fail(w, r, "ایجاد درخواست انجام نشد", 500)
		return
	}
	redirect(w, r, "/booking/"+id)
}
func (a *App) slots(ctx context.Context, b Booking) ([]Slot, error) {
	if !a.enabled(ctx, "service", b.Service) || (b.Service != "vet" && !a.enabled(ctx, "area", b.Area)) {
		return nil, nil
	}
	rows, err := a.DB.Query(ctx, `SELECT a.id,a.provider_id,a.day::text,a.period,to_char(a.start_time,'HH24:MI'),to_char(a.end_time,'HH24:MI'),a.capacity FROM availability a JOIN providers p ON p.id=a.provider_id WHERE p.active AND p.verified AND p.service=$1 AND $2=ANY(p.species) AND ($3='vet' OR $4=ANY(p.areas)) AND ($5='' OR p.id=$5) AND a.day>=(now() AT TIME ZONE 'Asia/Tehran')::date AND a.day<(now() AT TIME ZONE 'Asia/Tehran')::date+interval '14 days' AND a.capacity>(SELECT count(*) FROM bookings b WHERE b.availability_id=a.id AND b.payment_status='paid' AND b.status NOT IN ('cancelled','cancel_requested')) ORDER BY a.day,a.start_time`, b.Service, b.Species, b.Service, b.Area, b.ProviderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var slots []Slot
	for rows.Next() {
		var s Slot
		if err = rows.Scan(&s.ID, &s.ProviderID, &s.Day, &s.Period, &s.StartTime, &s.EndTime, &s.Capacity); err != nil {
			return nil, err
		}
		a.slotTimes(&s)
		if s.End.After(time.Now()) {
			slots = append(slots, s)
		}
	}
	return slots, rows.Err()
}
func (a *App) bookingData(r *http.Request, b Booking, msg string) Page {
	p := a.page(r, service(b.Service).Name)
	p.Booking = &b
	p.Service = service(b.Service)
	p.Error = msg
	providers, _ := a.providers(r.Context(), b.Service)
	for _, v := range providers {
		if contains(v.Species, b.Species) && (b.Service == "vet" || contains(v.Areas, b.Area)) {
			p.Providers = append(p.Providers, v)
		}
	}
	p.Slots, _ = a.slots(r.Context(), b)
	return p
}
func (a *App) bookingPage(w http.ResponseWriter, r *http.Request) {
	b, err := a.getBooking(r)
	if err != nil || !owned(r, b) {
		a.fail(w, r, "درخواست پیدا نشد", 404)
		return
	}
	if b.Status != "draft" {
		redirect(w, r, "/bookings/"+b.ID)
		return
	}
	if b.Stage == 4 && sess(r).User == nil {
		redirect(w, r, "/login?next=/booking/"+b.ID)
		return
	}
	a.render(w, r, "booking", a.bookingData(r, b, ""))
}
func (a *App) bookingStep(w http.ResponseWriter, r *http.Request) {
	b, err := a.getBooking(r)
	if err != nil || !owned(r, b) || b.Status != "draft" {
		a.fail(w, r, "درخواست قابل ویرایش نیست", 404)
		return
	}
	step, _ := strconv.Atoi(r.FormValue("step"))
	bad := func(msg string) { a.render(w, r, "booking", a.bookingData(r, b, msg)) }
	if step != b.Stage {
		redirect(w, r, "/booking/"+b.ID)
		return
	}
	if r.FormValue("back") == "1" {
		if b.Stage > 1 {
			a.DB.Exec(r.Context(), "UPDATE bookings SET stage=stage-1 WHERE id=$1 AND status='draft'", b.ID)
		}
		redirect(w, r, "/booking/"+b.ID)
		return
	}
	switch step {
	case 1:
		b.PetName = strings.TrimSpace(r.FormValue("pet_name"))
		b.Species = r.FormValue("species")
		b.Age = normalize(r.FormValue("age"))
		b.Weight = normalize(r.FormValue("weight"))
		if b.PetName == "" || len(b.PetName) > 120 || !contains([]string{"cat", "dog"}, b.Species) {
			bad("نام و نوع حیوان را درست وارد کنید.")
			return
		}
		b.Answers = map[string]string{"symptom": r.FormValue("symptom"), "duration": r.FormValue("duration"), "appetite": r.FormValue("appetite"), "history": r.FormValue("history"), "behavior": r.FormValue("behavior")}
		if b.Service == "vet" {
			b.Answers = map[string]string{}
			symptom := r.FormValue("symptom")
			qs := a.questions(r.Context())
			if len(qs) == 0 {
				bad("پرسشنامه در دسترس نیست؛ دوباره تلاش کنید.")
				return
			}
			for _, q := range qs {
				if q.When != "" && !contains(strings.Split(q.When, ","), symptom) {
					continue
				}
				v := strings.TrimSpace(r.FormValue(q.ID))
				if q.Required && v == "" {
					bad("پاسخ «" + q.Label + "» لازم است.")
					return
				}
				if q.Type == "select" && v != "" {
					valid := false
					for _, o := range q.Options {
						if o.Value == v {
							valid = true
						}
					}
					if !valid {
						bad("پاسخ نامعتبر برای «" + q.Label + "»")
						return
					}
				}
				if len(v) > 8000 {
					bad("شرح علائم بیش از حد طولانی است")
					return
				}
				b.Answers[q.ID] = v
			}
		}
		b.Consent = r.FormValue("consent") == "on"
		b.Sample = r.FormValue("sample")
		if b.Service == "vet" && (!b.Consent || b.Answers["symptom"] == "") {
			bad("علائم و پذیرش محدودیت مشاورهٔ آنلاین را تکمیل کنید.")
			return
		}
		if b.Service == "sampling" && !contains([]string{"blood", "urine", "stool"}, b.Sample) {
			bad("نمونه‌های برشی و بیوپسی فقط در کلینیک انجام می‌شوند. نوع نمونهٔ ساده را انتخاب کنید یا از پشتیبانی راهنمایی بگیرید.")
			return
		}
		b.Area = r.FormValue("area")
		b.Address = strings.TrimSpace(r.FormValue("address"))
		if b.Service != "vet" && (!a.enabled(r.Context(), "area", b.Area) || len(b.Address) < 10) {
			bad("محلهٔ تحت پوشش و آدرس کامل لازم است.")
			return
		}
		answers, _ := json.Marshal(b.Answers)
		_, err = a.DB.Exec(r.Context(), "UPDATE bookings SET pet_name=$2,species=$3,age=$4,weight=$5,answers=$6,consent=$7,sample=$8,area=$9,address=$10,stage=2,provider_id=NULL,availability_id=NULL WHERE id=$1 AND status='draft'", b.ID, b.PetName, b.Species, b.Age, b.Weight, answers, b.Consent, b.Sample, b.Area, b.Address)
	case 2:
		if b.Service == "sampling" {
			b.ProviderID = ""
		} else {
			b.ProviderID = r.FormValue("provider")
			ps, _ := a.providers(r.Context(), b.Service)
			valid := false
			for _, p := range ps {
				if p.ID == b.ProviderID && p.Verified && contains(p.Species, b.Species) && (b.Service == "vet" || contains(p.Areas, b.Area)) {
					valid = true
				}
			}
			if !valid {
				bad("ارائه‌دهندهٔ در دسترس را انتخاب کنید.")
				return
			}
		}
		_, err = a.DB.Exec(r.Context(), "UPDATE bookings SET provider_id=NULLIF($2,''),availability_id=NULL,stage=3 WHERE id=$1 AND status='draft'", b.ID, b.ProviderID)
	case 3:
		b.Items = r.Form["items"]
		amount, priceErr := a.price(r.Context(), b)
		if priceErr != nil {
			bad(priceErr.Error())
			return
		}
		slots, _ := a.slots(r.Context(), b)
		valid := false
		selected := r.FormValue("slot")
		for _, s := range slots {
			if s.ID == selected {
				valid = true
				b.ProviderID = s.ProviderID
			}
		}
		if !valid {
			bad("بازهٔ در دسترس را انتخاب کنید؛ ممکن است ظرفیت آن تکمیل شده باشد.")
			return
		}
		if b.Items == nil {
			b.Items = []string{}
		}
		_, err = a.DB.Exec(r.Context(), "UPDATE bookings SET availability_id=$2,provider_id=$3,items=$4,amount=$5,stage=4 WHERE id=$1 AND status='draft'", b.ID, selected, b.ProviderID, b.Items, amount)
	default:
		bad("مرحلهٔ نامعتبر")
		return
	}
	if err != nil {
		a.fail(w, r, "ذخیرهٔ درخواست انجام نشد", 500)
		return
	}
	redirect(w, r, "/booking/"+b.ID)
}
func (a *App) price(ctx context.Context, b Booking) (int64, error) {
	if !a.enabled(ctx, "service", b.Service) {
		return 0, fmt.Errorf("خدمت غیرفعال است")
	}
	switch b.Service {
	case "vet":
		var price int64
		err := a.DB.QueryRow(ctx, "SELECT price FROM providers WHERE id=$1 AND active AND verified AND service='vet'", b.ProviderID).Scan(&price)
		return price, err
	case "grooming":
		var total int64
		seen := map[string]bool{}
		for _, id := range b.Items {
			if seen[id] {
				return 0, fmt.Errorf("خدمت تکراری انتخاب شده")
			}
			seen[id] = true
			found := false
			for _, m := range a.liveMenu(ctx, "grooming") {
				if id == m.ID {
					total += m.Price
					found = true
				}
			}
			if !found {
				return 0, fmt.Errorf("خدمت نامعتبر")
			}
		}
		if total == 0 {
			return 0, fmt.Errorf("حداقل یک خدمت گرومینگ انتخاب کنید")
		}
		return total, nil
	case "sampling":
		for _, s := range a.liveMenu(ctx, "sampling") {
			if s.ID == b.Sample {
				return s.Price, nil
			}
		}
	}
	return 0, fmt.Errorf("نوع خدمت نامعتبر")
}
func (a *App) pay(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
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
	if err != nil || b.OwnerID != sess(r).User.ID {
		a.fail(w, r, "رزرو پیدا نشد", 404)
		return
	}
	if b.PaymentStatus == "paid" {
		redirect(w, r, "/bookings/"+b.ID)
		return
	}
	if b.Stage != 4 || b.PaymentKey != r.FormValue("payment_key") {
		a.fail(w, r, "رزرو آمادهٔ پرداخت نیست", 400)
		return
	}
	if r.FormValue("outcome") == "failed" {
		redirect(w, r, noticeURL("/booking/"+b.ID, "پرداخت آزمایشی ناموفق بود؛ می‌توانید دوباره تلاش کنید."))
		return
	}
	// Keep prices, commission, coverage and provider eligibility stable through payment.
	lockRows, lockErr := tx.Query(ctx, "SELECT id FROM business_config WHERE kind IN ('service','area','grooming','sampling') ORDER BY kind,id FOR SHARE")
	if lockErr != nil {
		a.fail(w, r, "تنظیمات پرداخت در دسترس نیست", 503)
		return
	}
	for lockRows.Next() {
	}
	lockErr = lockRows.Err()
	lockRows.Close()
	if lockErr == nil {
		var provider string
		lockErr = tx.QueryRow(ctx, "SELECT id FROM providers WHERE id=$1 FOR SHARE", b.ProviderID).Scan(&provider)
	}
	if lockErr != nil {
		a.fail(w, r, "ارائه‌دهنده در دسترس نیست", 409)
		return
	}
	var capacity int
	var providerID, day, prd string
	err = tx.QueryRow(ctx, "SELECT capacity,provider_id,day::text,period FROM availability WHERE id=$1 FOR UPDATE", b.AvailabilityID).Scan(&capacity, &providerID, &day, &prd)
	var used int
	tx.QueryRow(ctx, "SELECT count(*) FROM bookings WHERE availability_id=$1 AND payment_status='paid' AND status NOT IN ('cancelled','cancel_requested')", b.AvailabilityID).Scan(&used)
	slots, slotErr := a.slots(ctx, b)
	valid := false
	for _, s := range slots {
		if s.ID == b.AvailabilityID {
			valid = true
		}
	}
	price, priceErr := a.price(ctx, b)
	if err != nil || slotErr != nil || !valid || used >= capacity || providerID != b.ProviderID || priceErr != nil || price != b.Amount {
		a.fail(w, r, "ظرفیت یا قیمت تغییر کرده است؛ به مرحلهٔ انتخاب بازه برگردید.", 409)
		return
	}
	_, err = tx.Exec(ctx, "UPDATE bookings SET payment_status='paid',status='paid',paid_at=now(),commission_bps=COALESCE((SELECT (data->>'commission')::integer FROM business_config WHERE kind='service' AND id=bookings.service),0) WHERE id=$1", b.ID)
	if err == nil {
		err = addEvent(ctx, tx, b.ID, sess(r).User.ID, "paid", "پرداخت آزمایشی ثبت شد؛ درخواست در صف هماهنگی عملیات است.")
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		a.fail(w, r, "پرداخت ثبت نشد", 500)
		return
	}
	redirect(w, r, "/bookings/"+b.ID)
}
func (a *App) dashboard(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	p := a.page(r, "حساب من")
	var err error
	p.Bookings, err = a.bookings(r.Context(), "owner_id=$1 AND status!='draft'", p.User.ID)
	if err != nil {
		a.fail(w, r, "بارگذاری رزروها انجام نشد", 500)
		return
	}
	rows, err := a.DB.Query(r.Context(), "SELECT id,name,species,age,weight FROM pets WHERE user_id=$1 ORDER BY name", p.User.ID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, name, sp, age, weight string
			rows.Scan(&id, &name, &sp, &age, &weight)
			p.Pets = append(p.Pets, map[string]string{"id": id, "name": name, "species": sp, "age": age, "weight": weight})
		}
	}
	p.Chats, err = a.chatSummaries(r, "")
	if err != nil {
		a.fail(w, r, "پروندهٔ گفتگو بارگذاری نشد", 500)
		return
	}
	a.render(w, r, "dashboard", p)
}
func (a *App) detail(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	b, err := a.getBooking(r)
	if err != nil || b.OwnerID != sess(r).User.ID {
		a.fail(w, r, "رزرو پیدا نشد", 404)
		return
	}
	p := a.page(r, "جزئیات رزرو")
	p.Booking = &b
	p.Providers, _ = a.providers(r.Context(), b.Service)
	rows, err := a.DB.Query(r.Context(), "SELECT event,detail,created_at FROM booking_events WHERE booking_id=$1 ORDER BY id", b.ID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var e Event
			rows.Scan(&e.Event, &e.Detail, &e.At)
			p.Events = append(p.Events, e)
		}
	}
	p.Chats, err = a.chatSummaries(r, b.ID)
	if err != nil {
		a.fail(w, r, "پروندهٔ گفتگو بارگذاری نشد", 500)
		return
	}
	a.render(w, r, "detail", p)
}
func (a *App) cancel(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
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
	if err != nil || b.OwnerID != sess(r).User.ID {
		a.fail(w, r, "رزرو پیدا نشد", 404)
		return
	}
	if !contains([]string{"paid", "confirmed", "assigned", "accepted", "replacement_pending"}, b.Status) {
		a.fail(w, r, "برای این مرحله از پشتیبانی درخواست بررسی ثبت کنید.", 409)
		return
	}
	amount := refundable(b, time.Now(), b.Status == "replacement_pending")
	_, err = tx.Exec(ctx, "UPDATE bookings SET status='cancel_requested',refund_amount=$2,refund_status='pending_refund',refund_reason=$3 WHERE id=$1", b.ID, amount, r.FormValue("reason"))
	if err == nil {
		err = addEvent(ctx, tx, b.ID, sess(r).User.ID, "cancel_requested", "درخواست کنسلی ثبت شد؛ مبلغ قابل بازگشت: "+money(amount)+" تومان")
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		a.fail(w, r, "کنسلی ثبت نشد", 500)
		return
	}
	redirect(w, r, "/bookings/"+b.ID)
}
func (a *App) replacement(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
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
	if err != nil || b.OwnerID != sess(r).User.ID || b.Status != "replacement_pending" {
		a.fail(w, r, "پیشنهاد جایگزین موجود نیست", 409)
		return
	}
	_, err = tx.Exec(ctx, "UPDATE bookings SET provider_id=replacement_id,replacement_id=NULL,status='paid',availability_id=NULL,exact_time=NULL,meeting_url='' WHERE id=$1", b.ID)
	if err == nil {
		err = addEvent(ctx, tx, b.ID, sess(r).User.ID, "paid", "مشتری دامپزشک جایگزین را پذیرفت؛ هماهنگی مجدد لازم است.")
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	redirect(w, r, "/bookings/"+b.ID)
}
func (a *App) review(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	b, err := a.getBooking(r)
	rating, _ := strconv.Atoi(r.FormValue("rating"))
	if err != nil || b.OwnerID != sess(r).User.ID || !contains([]string{"completed", "result_ready"}, b.Status) || rating < 1 || rating > 5 {
		a.fail(w, r, "امکان ثبت نظر وجود ندارد", 400)
		return
	}
	_, err = a.DB.Exec(r.Context(), "INSERT INTO reviews(booking_id,user_id,rating,body) VALUES($1,$2,$3,$4) ON CONFLICT(booking_id) DO UPDATE SET rating=excluded.rating,body=excluded.body", b.ID, sess(r).User.ID, rating, r.FormValue("body"))
	if err != nil {
		a.fail(w, r, "نظر ثبت نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/bookings/"+b.ID, "ممنون! بازخورد شما برای بهبود خدمت ثبت شد."))
}
func (a *App) addPet(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	sp := r.FormValue("species")
	if name == "" || !contains([]string{"cat", "dog"}, sp) {
		a.fail(w, r, "مشخصات پت را تکمیل کنید", 400)
		return
	}
	_, err := a.DB.Exec(r.Context(), "INSERT INTO pets(id,user_id,name,species,age,weight) VALUES($1,$2,$3,$4,$5,$6)", token(), sess(r).User.ID, name, sp, r.FormValue("age"), r.FormValue("weight"))
	if err != nil {
		a.fail(w, r, "ذخیرهٔ پت انجام نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/dashboard", "پت شما اضافه شد."))
}
