package app

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type CareFollowup struct {
	ID, SourceBookingID, SourceChatID, OwnerID, DoctorID, ProviderID, PetID, Service, Day, Period, Note, State, BookingID, PetName, Species, DoctorName, BookingStatus string
	PreferredAt, ExactTime                                                                                                                                             *time.Time
	At                                                                                                                                                                 time.Time
}
type FollowupPage struct {
	Items                     []CareFollowup
	Item                      *CareFollowup
	Service, Day, Period, Key string
	Slots                     []Slot
	Doctor, Owner, CanOffer   bool
}

const followupColumns = "f.id,f.source_booking_id,f.source_chat_id,f.owner_id,f.doctor_id,f.provider_id,COALESCE(f.pet_id,''),f.service,f.day::text,f.period,f.note,f.state,COALESCE(f.booking_id,''),s.pet_name,s.species,p.name,COALESCE(b.status,''),f.preferred_at,b.exact_time,f.created_at"
const followupJoin = " FROM care_followups f JOIN bookings s ON s.id=f.source_booking_id JOIN providers p ON p.id=f.provider_id LEFT JOIN bookings b ON b.id=f.booking_id "

func scanFollowup(row pgx.Row) (CareFollowup, error) {
	var f CareFollowup
	e := row.Scan(&f.ID, &f.SourceBookingID, &f.SourceChatID, &f.OwnerID, &f.DoctorID, &f.ProviderID, &f.PetID, &f.Service, &f.Day, &f.Period, &f.Note, &f.State, &f.BookingID, &f.PetName, &f.Species, &f.DoctorName, &f.BookingStatus, &f.PreferredAt, &f.ExactTime, &f.At)
	return f, e
}
func (a *App) careList(ctx context.Context, user, source string, all bool) ([]CareFollowup, error) {
	rows, e := a.DB.Query(ctx, "SELECT "+followupColumns+followupJoin+" WHERE ($3 OR f.owner_id=$1 OR f.doctor_id=$1) AND ($2='' OR f.source_booking_id=$2) ORDER BY f.created_at DESC,f.id LIMIT 100", user, source, all)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []CareFollowup
	for rows.Next() {
		f, e := scanFollowup(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
func (a *App) followupForm(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "provider") {
		return
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "بارگذاری نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	c, role, e := a.lockedChat(ctx, tx, r.PathValue("id"), sess(r).User)
	if e != nil || role != "vet" || c.Kind != "vet" || c.Status != "completed" || c.ClosedAt == nil {
		a.fail(w, r, "پیشنهاد پیگیری پس از پایان و ثبت گزارش همین ویزیت ممکن است", 409)
		return
	}
	var active bool
	e = tx.QueryRow(ctx, "SELECT active AND verified FROM providers WHERE id=$1 AND user_id=$2", c.ProviderID, sess(r).User.ID).Scan(&active)
	if e != nil || !active {
		a.fail(w, r, "حساب متخصص فعال نیست", 403)
		return
	}
	if e = tx.Commit(ctx); e != nil {
		a.fail(w, r, "بارگذاری نشد", 500)
		return
	}
	p := a.page(r, "پیشنهاد ادامهٔ مراقبت")
	p.Chat = &ChatView{ID: c.ID}
	v := &FollowupPage{Key: token(), Service: r.URL.Query().Get("service"), Day: r.URL.Query().Get("day"), Period: r.URL.Query().Get("period")}
	p.Care = v
	if !contains([]string{"vet", "grooming", "sampling"}, v.Service) {
		v.Service = "vet"
	}
	if v.Day == "" {
		v.Day = time.Now().In(a.Location).AddDate(0, 0, 1).Format("2006-01-02")
	}
	if !contains([]string{"morning", "noon", "afternoon", "night"}, v.Period) {
		v.Period = "morning"
	}
	b, e := scanBooking(a.DB.QueryRow(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE id=$1", c.BookingID))
	if e != nil {
		a.fail(w, r, "پرونده پیدا نشد", 404)
		return
	}
	p.Booking = &b
	if v.Service == "vet" {
		probe := Booking{Service: "vet", ProviderID: c.ProviderID, Species: b.Species, TimeFlow: true, Status: "draft"}
		slots, e := a.exactSlots(ctx, probe)
		if e != nil {
			a.fail(w, r, "زمان‌های پزشک بارگذاری نشد", 500)
			return
		}
		if day, err := parseCalendar(v.Day, a.Location); err == nil {
			v.Day = day.Format("2006-01-02")
		}
		for _, s := range slots {
			if s.Day == v.Day && s.Period == v.Period {
				v.Slots = append(v.Slots, s)
			}
		}
	}
	if r.Header.Get("HX-Request") == "true" {
		a.renderFragment(w, r, "followup-time-options", "#followup-time-options", p)
		return
	}
	a.render(w, r, "followup-form", p)
}
func (a *App) proposeFollowup(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "provider") {
		return
	}
	ctx := r.Context()
	svc, pr := r.FormValue("service"), r.FormValue("period")
	note := strings.TrimSpace(r.FormValue("note"))
	key := r.FormValue("form_key")
	day, e := parseCalendar(r.FormValue("day"), a.Location)
	today := time.Now().In(a.Location).Format("2006-01-02")
	if e != nil || day.Format("2006-01-02") < today || day.Format("2006-01-02") > time.Now().In(a.Location).AddDate(0, 0, 90).Format("2006-01-02") || !contains([]string{"vet", "grooming", "sampling"}, svc) || !contains([]string{"morning", "noon", "afternoon", "night"}, pr) || utf8.RuneCountInString(note) > 2000 || len(key) != 64 || !a.enabled(ctx, "service", svc) {
		a.fail(w, r, "نوع خدمت، روز تا ۹۰ روز آینده و بازهٔ معتبر انتخاب کنید؛ توضیح حداکثر ۲۰۰۰ نویسه باشد", 400)
		return
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	fail := func(msg string, code int) { tx.Rollback(ctx); a.fail(w, r, msg, code) }
	c, role, e := a.lockedChat(ctx, tx, r.PathValue("id"), sess(r).User)
	if e != nil || role != "vet" || c.Kind != "vet" || c.Status != "completed" || c.ClosedAt == nil {
		fail("فقط پزشک همین ویزیتِ تکمیل‌شده می‌تواند پیشنهاد ثبت کند", 403)
		return
	}
	b, e := scanBooking(tx.QueryRow(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE id=$1", c.BookingID))
	if e != nil || b.PaymentStatus != "paid" {
		fail("پرونده معتبر نیست", 409)
		return
	}
	var active, report bool
	e = tx.QueryRow(ctx, "SELECT active AND verified,EXISTS(SELECT 1 FROM consultation_vet_forms WHERE chat_id=$2) FROM providers WHERE id=$1 AND user_id=$3 FOR SHARE", b.ProviderID, c.ID, sess(r).User.ID).Scan(&active, &report)
	if e != nil || !active || !report {
		fail("پزشک فعال و گزارش پزشکی ثبت‌شده لازم است", 409)
		return
	}
	var oldID, oldKey, oldService, oldSource string
	e = tx.QueryRow(ctx, "SELECT id,form_key,service,source_booking_id FROM care_followups WHERE form_key=$1 OR (source_booking_id=$2 AND service=$3 AND state IN ('offered','started')) LIMIT 1 FOR UPDATE", key, b.ID, svc).Scan(&oldID, &oldKey, &oldService, &oldSource)
	if e == nil {
		if oldKey == key && oldService == svc && oldSource == b.ID {
			redirect(w, r, "/follow-ups/"+oldID)
			return
		}
		fail("برای این نوع خدمت پیشنهاد فعالی وجود دارد؛ آن را از پرونده بررسی کنید", 409)
		return
	}
	if e != pgx.ErrNoRows {
		fail("ثبت نشد", 500)
		return
	}
	var at *time.Time
	if choice := r.FormValue("slot"); choice != "" {
		if svc != "vet" {
			fail("ساعت دقیق را مشتری پس از انتخاب متخصص تعیین می‌کند", 400)
			return
		}
		probe := Booking{Service: "vet", ProviderID: b.ProviderID, Species: b.Species, TimeFlow: true, Status: "draft"}
		slots, err := a.exactSlots(ctx, probe)
		if err != nil {
			fail("زمان‌ها بارگذاری نشد", 500)
			return
		}
		for _, s := range slots {
			if s.Choice() == choice && s.Day == day.Format("2006-01-02") && s.Period == pr {
				start := s.Start
				at = &start
				break
			}
		}
		if at == nil {
			fail("ساعت انتخابی دیگر آزاد نیست؛ بازه را دوباره بررسی کنید یا بدون ساعت مشخص پیشنهاد دهید", 409)
			return
		}
	}
	pet := b.Answers["_pet_id"]
	var owns bool
	if pet != "" {
		tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pets WHERE id=$1 AND user_id=$2)", pet, b.OwnerID).Scan(&owns)
		if !owns {
			pet = ""
		}
	}
	id := token()
	_, e = tx.Exec(ctx, "INSERT INTO care_followups(id,source_booking_id,source_chat_id,owner_id,doctor_id,provider_id,pet_id,service,day,period,preferred_at,note,form_key) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,$9,$10,$11,$12,$13)", id, b.ID, c.ID, b.OwnerID, sess(r).User.ID, b.ProviderID, pet, svc, day.Format("2006-01-02"), pr, at, note, key)
	if e == nil {
		e = careNotification(ctx, tx, id, b.ID, b.OwnerID, "پیشنهاد پیگیری پزشک")
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		fail("پیشنهاد ثبت نشد؛ پرونده را دوباره بررسی کنید", 409)
		return
	}
	redirect(w, r, "/follow-ups/"+id)
}
func careNotification(ctx context.Context, tx pgx.Tx, id, source, user, event string) error {
	_, e := tx.Exec(ctx, "INSERT INTO notifications(user_id,booking_id,kind,event,event_key,followup_id) VALUES($1,$2,'care_followup',$3,$4,$5) ON CONFLICT DO NOTHING", user, source, event, "care:"+id+":"+event+":"+user, id)
	return e
}
func (a *App) followupList(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	all := strings.HasPrefix(r.URL.Path, "/management/")
	if all && !a.auth(w, r, "ops", "admin") {
		return
	}
	p := a.page(r, "ادامهٔ مراقبت و پیگیری")
	v := &FollowupPage{}
	p.Care = v
	var e error
	v.Items, e = a.careList(r.Context(), sess(r).User.ID, "", all)
	if e != nil {
		a.fail(w, r, "پیشنهادها بارگذاری نشد", 500)
		return
	}
	a.render(w, r, "followup-list", p)
}
func (a *App) followupPage(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	f, e := scanFollowup(a.DB.QueryRow(r.Context(), "SELECT "+followupColumns+followupJoin+" WHERE f.id=$1", r.PathValue("id")))
	u := sess(r).User
	if e != nil || (u.ID != f.OwnerID && u.ID != f.DoctorID && !contains([]string{"admin", "ops"}, u.Role)) {
		a.fail(w, r, "پیشنهاد در دسترس نیست", 404)
		return
	}
	p := a.page(r, "پیشنهاد پیگیری "+f.PetName)
	p.Care = &FollowupPage{Item: &f, Owner: u.ID == f.OwnerID, Doctor: u.ID == f.DoctorID}
	a.render(w, r, "followup-detail", p)
}
func (a *App) followupAction(w http.ResponseWriter, r *http.Request) {
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
	// Lock only the recommendation here. Never wait on its child reservation while holding this lock.
	f, e := scanFollowup(tx.QueryRow(ctx, "SELECT "+followupColumns+followupJoin+" WHERE f.id=$1 FOR UPDATE OF f", r.PathValue("id")))
	u := sess(r).User
	if e != nil || (u.ID != f.OwnerID && u.ID != f.DoctorID) {
		fail("پیشنهاد در دسترس نیست", 404)
		return
	}
	act := r.FormValue("action")
	switch act {
	case "start":
		if u.ID != f.OwnerID {
			fail("ادامهٔ رزرو فقط با مشتری است", 403)
			return
		}
		if f.State == "started" && f.BookingID != "" {
			var state string
			tx.QueryRow(ctx, "SELECT status FROM bookings WHERE id=$1", f.BookingID).Scan(&state)
			redirect(w, r, careBookingURL(f.BookingID, state))
			return
		}
		if f.State != "offered" || !a.enabled(ctx, "service", f.Service) {
			fail("پیشنهاد برای رزرو فعال نیست", 409)
			return
		}
		src, e := scanBooking(tx.QueryRow(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE id=$1", f.SourceBookingID))
		if e != nil {
			fail("پرونده پیدا نشد", 404)
			return
		}
		answers := map[string]string{"birth_year": src.Answers["birth_year"]}
		name, sp, age, weight := src.PetName, src.Species, src.Age, src.Weight
		if f.PetID != "" {
			var year string
			e = tx.QueryRow(ctx, "SELECT name,species,age,weight,birth_year FROM pets WHERE id=$1 AND user_id=$2", f.PetID, f.OwnerID).Scan(&name, &sp, &age, &weight, &year)
			if e != nil {
				fail("پروفایل پت در دسترس نیست", 409)
				return
			}
			answers["_pet_id"] = f.PetID
			answers["birth_year"] = year
		}
		id := token()
		_, e = tx.Exec(ctx, "INSERT INTO bookings(id,session_token,owner_id,service,stage,pet_name,species,age,weight,answers,area,address,payment_key,request_flow,time_flow,fulfillment_flow,lab_flow,followup_id) VALUES($1,$2,$3,$4,1,$5,$6,$7,$8,$9,$10,$11,$12,true,true,true,$4='sampling',$13)", id, sess(r).Token, f.OwnerID, f.Service, name, sp, age, weight, mustJSON(answers), src.Area, src.Address, token(), f.ID)
		if e == nil {
			_, e = tx.Exec(ctx, "UPDATE care_followups SET state='started',booking_id=$2,updated_at=now() WHERE id=$1", f.ID, id)
		}
		if e == nil {
			e = tx.Commit(ctx)
		}
		if e != nil {
			fail("شروع رزرو ثبت نشد", 409)
			return
		}
		redirect(w, r, "/booking/"+id)
		return
	case "dismiss":
		if u.ID != f.OwnerID {
			fail("این تصمیم مخصوص مشتری است", 403)
			return
		}
	case "withdraw":
		if u.ID != f.DoctorID || u.Role != "provider" {
			fail("لغو پیشنهاد فقط با پزشک پیشنهاددهنده است", 403)
			return
		}
	default:
		fail("اقدام معتبر انتخاب کنید", 400)
		return
	}
	if f.State != "offered" {
		fail("پس از شروع رزرو، ادامه و لغو از همان درخواست انجام می‌شود", 409)
		return
	}
	state, event, target := "dismissed", "پیشنهاد پیگیری پذیرفته نشد", f.DoctorID
	if act == "withdraw" {
		state, event, target = "withdrawn", "پیشنهاد پیگیری پس گرفته شد", f.OwnerID
	}
	_, e = tx.Exec(ctx, "UPDATE care_followups SET state=$2,updated_at=now() WHERE id=$1", f.ID, state)
	if e == nil {
		e = careNotification(ctx, tx, f.ID, f.SourceBookingID, target, event)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		fail("ثبت نشد", 409)
		return
	}
	redirect(w, r, "/follow-ups/"+f.ID)
}
func careBookingURL(id, state string) string {
	if state == "draft" || state == "awaiting_payment" {
		return "/booking/" + id
	}
	return "/bookings/" + id
}
func (a *App) followupPetMatches(ctx context.Context, b Booking, id, name, sp string) bool {
	if b.FollowupID == "" {
		return true
	}
	f, e := scanFollowup(a.DB.QueryRow(ctx, "SELECT "+followupColumns+followupJoin+" WHERE f.id=$1", b.FollowupID))
	if e != nil {
		return false
	}
	if f.PetID != "" {
		return id == f.PetID
	}
	return name == f.PetName && sp == f.Species
}
func (a *App) followupBookingContext(r *http.Request, b *Booking, p *Page) *http.Request {
	if b.FollowupID == "" {
		return r
	}
	f, e := scanFollowup(a.DB.QueryRow(r.Context(), "SELECT "+followupColumns+followupJoin+" WHERE f.id=$1", b.FollowupID))
	if e != nil {
		return r
	}
	p.Care = &FollowupPage{Item: &f}
	if b.Stage == 2 && b.Service == "vet" && b.ProviderID == "" {
		for _, v := range p.Providers {
			if v.ID == f.ProviderID && v.Verified {
				b.ProviderID = f.ProviderID
				break
			}
		}
	}
	if b.Stage == 3 && b.ExactTime == nil && r.URL.Query().Get("filter") == "" && r.URL.Query().Get("day") == "" {
		copy := r.Clone(r.Context())
		u := *r.URL
		q := url.Values{}
		q.Set("day", f.Day)
		q.Set("period", f.Period)
		if f.PreferredAt != nil {
			q.Set("at", f.PreferredAt.In(a.Location).Format("15:04"))
		}
		u.RawQuery = q.Encode()
		copy.URL = &u
		return copy
	}
	return r
}
