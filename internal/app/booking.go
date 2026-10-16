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
	if p.User != nil {
		p.Wallet = &WalletPage{}
		if e := a.DB.QueryRow(r.Context(), "SELECT cash,bonus,frozen FROM wallets WHERE user_id=$1", p.User.ID).Scan(&p.Wallet.Account.Cash, &p.Wallet.Account.Bonus, &p.Wallet.Account.Frozen); e != nil {
			p.Wallet = nil
		}
	}
	p.Form = a.form(r.Context(), b.Service, b.Answers)
	p.PetForm = a.form(r.Context(), "pet", petValues(b))
	if b.Answers["_pet_id"] != "" && sess(r).User != nil {
		stored := a.savedForm(r.Context(), "pet", b.Answers["_pet_id"])
		for k, v := range petValues(b) {
			stored.Values[k] = v
		}
		p.PetForm.Values = stored.Values
		missing := []Question{}
		for _, q := range p.PetForm.Questions {
			if !reusePetAnswer(q, stored) && !contains([]string{"pet_name", "species", "birth_year", "age", "weight"}, q.ID) {
				missing = append(missing, q)
			}
		}
		if b.Age == "" && b.Answers["birth_year"] == "" {
			missing = append(missing, textQ("birth_year", "سال تولد شمسی", false), textQ("age", "سن تقریبی اگر سال تولد را نمی‌دانید", false))
		}
		p.PetForm.Questions = missing
	}
	if sess(r).User != nil {
		p.Pets = a.pets(r.Context(), sess(r).User.ID)
	}
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
	bad := func(msg string) {
		if b.Answers == nil {
			b.Answers = map[string]string{}
		}
		for _, q := range a.form(r.Context(), b.Service, nil).Questions {
			b.Answers[q.ID] = r.FormValue(q.ID)
		}
		b.Answers["birth_year"] = r.FormValue("birth_year")
		a.render(w, r, "booking", a.bookingData(r, b, msg))
	}
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
		selectedPet := b.Answers["_pet_id"]
		if e := a.stepPet(r, &b); e != nil {
			bad(e.Error())
			return
		}
		petAnswers, e := formAnswers(r, a.form(r.Context(), "pet", nil))
		if e != nil {
			bad(e.Error())
			return
		}
		f := a.form(r.Context(), b.Service, nil)
		answers, e := formAnswers(r, f)
		if e != nil {
			bad(e.Error())
			return
		}
		b.Answers = answers
		b.Answers["birth_year"] = petAnswers["birth_year"]
		b.Answers["_pet_id"] = selectedPet
		b.Answers["_questions"] = string(mustJSON(f.Questions))
		b.Answers["_pet_questions"] = string(mustJSON(a.form(r.Context(), "pet", nil).Questions))
		b.Answers["_pet_answers"] = string(mustJSON(petAnswers))
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
		answersRaw, _ := json.Marshal(b.Answers)
		tx, e := a.DB.Begin(r.Context())
		if e != nil {
			bad("ذخیره نشد")
			return
		}
		defer tx.Rollback(r.Context())
		if sess(r).User != nil {
			petID := selectedPet
			if petID == "" {
				petID = token()
				_, err = tx.Exec(r.Context(), "INSERT INTO pets(id,user_id,name,species,age,weight,birth_year) VALUES($1,$2,$3,$4,$5,$6,$7)", petID, sess(r).User.ID, b.PetName, b.Species, b.Age, b.Weight, petAnswers["birth_year"])
			} else {
				_, err = tx.Exec(r.Context(), "UPDATE pets SET age=$2,birth_year=$3 WHERE id=$1 AND user_id=$4", petID, b.Age, petAnswers["birth_year"], sess(r).User.ID)
			}
			if err == nil {
				err = saveFormResponse(r.Context(), tx, a.form(r.Context(), "pet", nil), petID, sess(r).User.ID, petAnswers)
			}
			b.Answers["_pet_id"] = petID
			answersRaw, _ = json.Marshal(b.Answers)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), "UPDATE bookings SET pet_name=$2,species=$3,age=$4,weight=$5,answers=$6,consent=$7,sample=$8,area=$9,address=$10,stage=2,provider_id=NULL,availability_id=NULL WHERE id=$1 AND status='draft'", b.ID, b.PetName, b.Species, b.Age, b.Weight, answersRaw, b.Consent, b.Sample, b.Area, b.Address)
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
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
	var walletCash, walletBonus int64
	if v := normalizeNumber(r.FormValue("wallet_amount")); v != "" && v != "0" {
		amount, e := walletAmount(v)
		if e != nil || amount > b.Amount {
			a.fail(w, r, "مبلغ کیف پول معتبر و حداکثر برابر مبلغ رزرو باشد", 400)
			return
		}
		wallet, e := walletLock(ctx, tx, b.OwnerID)
		if e != nil || wallet.Frozen || wallet.Cash+wallet.Bonus < amount {
			a.fail(w, r, "کیف پول مسدود است یا موجودی کافی نیست", 409)
			return
		}
		walletBonus = min(wallet.Bonus, amount)
		walletCash = amount - walletBonus
		if e = walletPost(ctx, tx, b.OwnerID, "booking_payment", -walletCash, -walletBonus, 0, "booking:"+b.ID, b.ID, b.OwnerID, "پرداخت رزرو از کیف پول؛ باقی مبلغ شبیه‌سازی می‌شود"); e != nil {
			a.fail(w, r, "پرداخت کیف پول ثبت نشد", 409)
			return
		}
	}
	_, err = tx.Exec(ctx, "UPDATE bookings SET wallet_cash_used=$2,wallet_bonus_used=$3 WHERE id=$1", b.ID, walletCash, walletBonus)
	if err != nil {
		a.fail(w, r, "پرداخت ثبت نشد", 500)
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
	p.Pets = a.pets(r.Context(), p.User.ID)
	p.PetForm = a.form(r.Context(), "pet", map[string]string{})
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
	p.Form = a.form(r.Context(), "service-review", nil)
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
	rating, _ := strconv.Atoi(normalize(r.FormValue("rating")))
	if err != nil || b.OwnerID != sess(r).User.ID || !contains([]string{"completed", "result_ready"}, b.Status) || rating < 1 || rating > 5 {
		a.fail(w, r, "امکان ثبت نظر وجود ندارد", 400)
		return
	}
	f := a.form(r.Context(), "service-review", nil)
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
	_, err = tx.Exec(r.Context(), "INSERT INTO reviews(booking_id,user_id,rating,body) VALUES($1,$2,$3,$4) ON CONFLICT(booking_id) DO UPDATE SET rating=excluded.rating,body=excluded.body,published=false", b.ID, sess(r).User.ID, rating, r.FormValue("body"))
	if err == nil {
		err = saveFormResponse(r.Context(), tx, f, b.ID, sess(r).User.ID, answers)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
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
	name := strings.TrimSpace(r.FormValue("pet_name"))
	if name == "" {
		name = strings.TrimSpace(r.FormValue("name"))
		r.Form.Set("pet_name", name)
	}
	sp := r.FormValue("species")
	age, year, e := petBirth(r.FormValue("age"), r.FormValue("birth_year"))
	if e != nil {
		a.fail(w, r, e.Error(), 400)
		return
	}
	r.Form.Set("age", age)
	r.Form.Set("birth_year", year)
	f := a.form(r.Context(), "pet", nil)
	vs, e := formAnswers(r, f)
	if e != nil || name == "" || !contains([]string{"cat", "dog"}, sp) {
		a.fail(w, r, "مشخصات پت را کامل کنید", 400)
		return
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(r.Context())
	id := token()
	_, err := tx.Exec(r.Context(), "INSERT INTO pets(id,user_id,name,species,age,weight,birth_year) VALUES($1,$2,$3,$4,$5,$6,$7)", id, sess(r).User.ID, name, sp, age, r.FormValue("weight"), year)
	if err == nil {
		err = saveFormResponse(r.Context(), tx, f, id, sess(r).User.ID, vs)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.fail(w, r, "ذخیرهٔ پت انجام نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/dashboard", "پت شما اضافه شد."))
}
