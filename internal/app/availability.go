package app

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

func (a *App) slotTimes(s *Slot) {
	s.Start, _ = time.ParseInLocation("2006-01-02T15:04", s.Day+"T"+s.StartTime, a.Location)
	s.End, _ = time.ParseInLocation("2006-01-02T15:04", s.Day+"T"+s.EndTime, a.Location)
}

// Capacity includes paid reservations, while an existing booking keeps its own
// reserved place when operations assigns an exact time in that same window.
func (a *App) assignmentSlots(ctx context.Context, b Booking) ([]Slot, error) {
	rows, e := a.DB.Query(ctx, `SELECT a.id,a.provider_id,p.name,a.day::text,a.period,to_char(a.start_time,'HH24:MI'),to_char(a.end_time,'HH24:MI'),a.capacity,booking_window_peak(a.id,$1) FROM availability a JOIN providers p ON p.id=a.provider_id WHERE p.active AND p.verified AND p.service=$2 AND $3=ANY(p.species) AND ($2='vet' OR $4=ANY(p.areas)) AND ($2<>'vet' OR p.id=$5) AND a.day<=(now() AT TIME ZONE 'Asia/Tehran')::date+90 AND ((a.day+a.end_time) AT TIME ZONE 'Asia/Tehran')>now() AND a.capacity>booking_window_peak(a.id,$1) ORDER BY a.day,a.start_time,p.name,a.id`, b.ID, b.Service, b.Species, b.Area, b.ProviderID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Slot{}
	for rows.Next() {
		var s Slot
		e = rows.Scan(&s.ID, &s.ProviderID, &s.ProviderName, &s.Day, &s.Period, &s.StartTime, &s.EndTime, &s.Capacity, &s.Used)
		if e != nil {
			return nil, e
		}
		a.slotTimes(&s)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (a *App) saveAvailability(w http.ResponseWriter, r *http.Request, id, next string) {
	day, err := parseCalendar(r.FormValue("day"), a.Location)
	cap, e := strconv.Atoi(normalize(r.FormValue("capacity")))
	prd := r.FormValue("period")
	today, _ := time.ParseInLocation("2006-01-02", time.Now().In(a.Location).Format("2006-01-02"), a.Location)
	if err != nil || e != nil || cap < 0 || cap > 20 || !contains([]string{"morning", "noon", "afternoon", "night", "custom"}, prd) || day.Before(today) || day.After(today.AddDate(0, 0, 90)) {
		a.fail(w, r, "تاریخ امروز تا ۹۰ روز آینده، بازه و ظرفیت صفر تا ۲۰ وارد کنید", 400)
		return
	}
	start, end := normalize(r.FormValue("start_time")), normalize(r.FormValue("end_time"))
	if prd != "custom" {
		start = map[string]string{"morning": "09:00", "noon": "12:00", "afternoon": "15:00", "night": "18:00"}[prd]
		end = map[string]string{"morning": "12:00", "noon": "15:00", "afternoon": "18:00", "night": "21:00"}[prd]
	}
	first, err := time.Parse("15:04", start)
	last, e := time.Parse("15:04", end)
	if err != nil || e != nil || !last.After(first) {
		a.fail(w, r, "ساعت شروع و پایان معتبر در همان روز وارد کنید؛ پایان باید بعد از شروع باشد", 400)
		return
	}
	finish := day.Add(time.Duration(last.Hour())*time.Hour + time.Duration(last.Minute())*time.Minute)
	if !finish.After(time.Now()) {
		a.fail(w, r, "این بازه تمام شده است", 400)
		return
	}
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		a.fail(w, r, "ثبت بازه انجام نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	if _, e := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,13013))", id); e != nil {
		a.fail(w, r, "بررسی ظرفیت انجام نشد", 500)
		return
	}
	var valid bool
	err = tx.QueryRow(ctx, "SELECT active AND verified FROM providers WHERE id=$1 FOR SHARE", id).Scan(&valid)
	if err != nil || !valid {
		a.fail(w, r, "متخصص فعال و تأییدشده انتخاب کنید", 400)
		return
	}
	_, err = tx.Exec(ctx, "INSERT INTO availability(id,provider_id,day,period,capacity,start_time,end_time) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING", token(), id, day.Format("2006-01-02"), prd, cap, start, end)
	var slot string
	if err == nil {
		err = tx.QueryRow(ctx, "SELECT id FROM availability WHERE provider_id=$1 AND day=$2 AND start_time=$3::time AND end_time=$4::time FOR UPDATE", id, day.Format("2006-01-02"), start, end).Scan(&slot)
	}
	var used int
	if err == nil {
		err = tx.QueryRow(ctx, "SELECT booking_window_peak($1,'')", slot).Scan(&used)
	}
	if err == nil && cap < used {
		a.fail(w, r, "ظرفیت نمی‌تواند کمتر از رزروهای ثبت‌شده باشد؛ برای تغییر رزرو با عملیات هماهنگ کنید", 409)
		return
	}
	if err == nil {
		_, err = tx.Exec(ctx, "UPDATE availability SET capacity=$2 WHERE id=$1", slot, cap)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		a.fail(w, r, "ثبت بازه انجام نشد", 500)
		return
	}
	redirect(w, r, noticeURL(next, "بازهٔ در دسترس ذخیره شد؛ مدیریت هنگام تخصیص از همین بازه‌ها انتخاب می‌کند."))
}

// Call only after locking the booking. Provider validity and the slot are checked
// under locks before capacity or assignment can change.
func (a *App) assignedWindow(ctx context.Context, tx pgx.Tx, b Booking, r *http.Request) (Slot, time.Time, string, int) {
	var s Slot
	var exact time.Time
	slotID := r.FormValue("availability")
	selected := r.FormValue("provider")
	if slotID != "" {
		e := tx.QueryRow(ctx, "SELECT provider_id,day::text FROM availability WHERE id=$1", slotID).Scan(&selected, &s.Day)
		if e != nil {
			return s, exact, "بازهٔ انتخابی پیدا نشد؛ صفحه را تازه کنید", 409
		}
		if r.FormValue("provider") != "" && r.FormValue("provider") != selected {
			return s, exact, "متخصص و بازهٔ انتخابی یکسان نیستند", 400
		}
		var e2 error
		exact, e2 = time.ParseInLocation("2006-01-02T15:04", s.Day+"T"+normalize(r.FormValue("exact_clock")), a.Location)
		if e2 != nil {
			return s, exact, "ساعت دقیق را به صورت ساعت و دقیقه وارد کنید", 400
		}
	} else {
		var e error
		exact, e = time.ParseInLocation("2006-01-02T15:04", normalize(r.FormValue("exact_time")), a.Location)
		if e != nil {
			return s, exact, "زمان دقیق معتبر وارد کنید", 400
		}
	}
	if !exact.After(time.Now()) {
		return s, exact, "زمان دقیق باید در آینده باشد", 400
	}
	if b.Service == "vet" && selected != b.ProviderID {
		return s, exact, "تعویض دامپزشک نیازمند پذیرش مشتری است", 409
	}
	var active, verified bool
	var svc string
	var species, areas []string
	e := tx.QueryRow(ctx, "SELECT active,verified,service,species,areas FROM providers WHERE id=$1 FOR SHARE", selected).Scan(&active, &verified, &svc, &species, &areas)
	if e != nil || !active || !verified || svc != b.Service || !contains(species, b.Species) || (svc != "vet" && !contains(areas, b.Area)) {
		return s, exact, "ارائه‌دهنده برای این درخواست صلاحیت یا پوشش ندارد", 400
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,13013))", selected); err != nil {
		return s, exact, "بررسی ظرفیت انجام نشد", 500
	}
	if slotID == "" {
		// Compatibility with existing clients: resolve a real registered window,
		// never manufacture one from an arbitrary operator-entered time.
		e = tx.QueryRow(ctx, `SELECT a.id FROM availability a WHERE a.provider_id=$1 AND a.day=$2 AND a.start_time<=$3::time AND a.end_time>$3::time AND a.capacity>(SELECT count(*) FROM bookings x WHERE x.availability_id=a.id AND x.id<>$4 AND booking_holds_capacity(x.payment_status,x.status,x.stage_deadline)) ORDER BY (a.id=$5) DESC,a.end_time-a.start_time,a.id LIMIT 1`, selected, exact.Format("2006-01-02"), exact.Format("15:04"), b.ID, b.AvailabilityID).Scan(&slotID)
		if e != nil {
			return s, exact, "برای این زمان بازهٔ آزاد ثبت نشده است؛ ابتدا متخصص بازه را ثبت کند و سپس از بازه‌های قابل تخصیص انتخاب کنید", 409
		}
	}
	e = tx.QueryRow(ctx, "SELECT id,provider_id,day::text,period,to_char(start_time,'HH24:MI'),to_char(end_time,'HH24:MI'),capacity FROM availability WHERE id=$1 FOR UPDATE", slotID).Scan(&s.ID, &s.ProviderID, &s.Day, &s.Period, &s.StartTime, &s.EndTime, &s.Capacity)
	if e != nil || s.ProviderID != selected {
		return s, exact, "بازهٔ انتخابی در دسترس نیست؛ صفحه را تازه کنید", 409
	}
	a.slotTimes(&s)
	if exact.Before(s.Start) || !exact.Before(s.End) {
		return s, exact, "ساعت دقیق باید داخل بازهٔ ثبت‌شدهٔ متخصص باشد", 400
	}
	e = tx.QueryRow(ctx, "SELECT count(*) FROM bookings WHERE availability_id=$1 AND id<>$2 AND booking_holds_capacity(payment_status,status,stage_deadline)", s.ID, b.ID).Scan(&s.Used)
	if e != nil || s.Used >= s.Capacity {
		return s, exact, "ظرفیت این بازه پر یا بسته شده است؛ صفحه را تازه کنید و بازهٔ دیگری انتخاب کنید", 409
	}
	var free bool
	if e = tx.QueryRow(ctx, "SELECT booking_time_available($1,$2,$3,$4,$5)", s.ProviderID, s.ID, exact, int(s.End.Sub(exact).Minutes()), b.ID).Scan(&free); e != nil || !free {
		return s, exact, "ظرفیت این زمان با رزرو دیگری تداخل دارد", 409
	}
	if !exact.After(time.Now()) {
		return s, exact, "زمان انتخابی گذشته است؛ ساعت آینده‌ای داخل بازه انتخاب کنید", 400
	}
	return s, exact, "", 0
}
