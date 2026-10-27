package app

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type TimeSuggestion struct{ Day, Clock, Period, URL string }
type ScheduleView struct {
	Day, Period, Selected, Error string
	Minutes                      int
	NeedItems                    bool
	Options                      []Slot
	Suggestions                  []TimeSuggestion
	Menu                         []TariffItem
}

func timePeriod(t time.Time) string {
	h := t.Hour()
	switch {
	case h < 6:
		return "night"
	case h < 12:
		return "morning"
	case h < 15:
		return "noon"
	case h < 18:
		return "afternoon"
	default:
		return "night"
	}
}
func (s Slot) Choice() string { return s.ID + "|" + s.Start.Format(time.RFC3339) }
func (a *App) exactSlots(ctx context.Context, b Booking) ([]Slot, error) {
	if !a.enabled(ctx, "service", b.Service) || (b.Service != "vet" && !a.enabled(ctx, "area", b.Area)) {
		return nil, nil
	}
	minutes := b.DurationMinutes
	if b.Status == "draft" || minutes == 0 {
		q, e := a.quote(ctx, b)
		if e != nil {
			return nil, e
		}
		minutes = q.Minutes
	}
	if minutes < 5 || minutes > 1440 {
		return nil, fmt.Errorf("مدت خدمت معتبر نیست")
	}
	rows, e := a.DB.Query(ctx, `SELECT a.id,a.provider_id,p.name,a.day::text,t::timestamptz,t::timestamptz+$6::integer*interval '1 minute',a.capacity,COALESCE(p.sampling_rate,0) FROM availability a JOIN providers p ON p.id=a.provider_id CROSS JOIN LATERAL generate_series((a.day+a.start_time) AT TIME ZONE 'Asia/Tehran',((a.day+a.end_time) AT TIME ZONE 'Asia/Tehran')-$6::integer*interval '1 minute',interval '15 minutes') t WHERE p.active AND p.verified AND p.service=$1 AND $2=ANY(p.species) AND ($1='vet' OR $3=ANY(p.areas)) AND ($4='' OR p.id=$4) AND a.day>=(now() AT TIME ZONE 'Asia/Tehran')::date AND a.day<=(now() AT TIME ZONE 'Asia/Tehran')::date+90 AND booking_time_available(p.id,a.id,t,$6,$5) ORDER BY t,p.id,(a.id=$7) DESC,a.id LIMIT 10000`, b.Service, b.Species, b.Area, b.ProviderID, b.ID, minutes, b.AvailabilityID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var labCost int64
	var rules BusinessData
	if b.LabFlow {
		q, e := a.quote(ctx, b)
		if e != nil {
			return nil, e
		}
		labCost = q.Lab.Cost
		rules, e = a.pricingRules(ctx, "sampling")
		if e != nil {
			return nil, e
		}
	}
	out := []Slot{}
	seen := map[string]bool{}
	for rows.Next() {
		var s Slot
		var samplingRate int64
		if e = rows.Scan(&s.ID, &s.ProviderID, &s.ProviderName, &s.Day, &s.Start, &s.End, &s.Capacity, &samplingRate); e != nil {
			return nil, e
		}
		if b.LabFlow {
			if samplingRate <= 0 {
				continue
			}
			q := PriceQuote{Base: samplingRate}
			if e := addPriceFees(&q, rules); e != nil {
				return nil, e
			}
			s.QuotedPrice = q.Total + labCost
		}
		s.Start = s.Start.In(a.Location)
		s.End = s.End.In(a.Location)
		key := s.ProviderID + "|" + s.Start.Format(time.RFC3339)
		if seen[key] {
			continue
		}
		seen[key] = true
		s.StartTime = s.Start.Format("15:04")
		s.EndTime = s.End.Format("15:04")
		s.Period = timePeriod(s.Start)
		out = append(out, s)
	}
	return out, rows.Err()
}
func (a *App) scheduleView(r *http.Request, b *Booking) *ScheduleView {
	v := &ScheduleView{}
	q := r.URL.Query()
	if b.Service == "grooming" {
		v.Menu, _ = a.groomTariffs(r.Context(), b.ProviderID)
		if q.Get("filter") == "1" {
			b.Items = q["items"]
		}
		if len(b.Items) == 0 {
			v.NeedItems = true
		}
	}
	quote, e := a.quote(r.Context(), *b)
	if e != nil && !v.NeedItems {
		v.Error = "زمان‌های این خدمت بارگذاری نشد؛ انتخاب خدمات را بررسی کنید."
	}
	v.Minutes = quote.Minutes
	all := []Slot{}
	if !v.NeedItems && e == nil {
		all, e = a.exactSlots(r.Context(), *b)
		if e != nil {
			v.Error = "زمان‌ها بارگذاری نشدند؛ دوباره تلاش کنید."
		}
	}
	day := q.Get("day")
	if day != "" {
		if d, e := parseCalendar(day, a.Location); e == nil {
			day = d.Format("2006-01-02")
		} else {
			v.Error = "روز معتبر را از تقویم انتخاب کنید"
			day = ""
		}
	}
	if day == "" {
		if b.ExactTime != nil {
			day = b.ExactTime.In(a.Location).Format("2006-01-02")
		} else if len(all) > 0 {
			day = all[0].Day
		} else {
			day = time.Now().In(a.Location).Format("2006-01-02")
		}
	}
	v.Day = day
	v.Period = q.Get("period")
	if !contains([]string{"morning", "noon", "afternoon", "night"}, v.Period) {
		v.Period = "morning"
		for _, s := range all {
			if s.Day == day {
				v.Period = s.Period
				break
			}
		}
	}
	clock := normalize(q.Get("at"))
	for _, s := range all {
		if s.Day == day && s.Period == v.Period {
			v.Options = append(v.Options, s)
			if (b.ExactTime != nil && s.Start.Equal(*b.ExactTime)) || s.StartTime == clock {
				v.Selected = s.Choice()
			}
		}
	}
	if len(v.Options) == 0 && len(all) > 0 {
		target, _ := time.ParseInLocation("2006-01-02T15:04", day+"T"+map[string]string{"morning": "09:00", "noon": "12:00", "afternoon": "15:00", "night": "18:00"}[v.Period], a.Location)
		distance := func(t time.Time) time.Duration {
			d := t.Sub(target)
			if d < 0 {
				return -d
			}
			return d
		}
		sort.SliceStable(all, func(i, j int) bool { return distance(all[i].Start) < distance(all[j].Start) })
		for _, s := range all {
			params := url.Values{"filter": {"1"}, "day": {s.Day}, "period": {s.Period}, "at": {s.StartTime}}
			for _, id := range b.Items {
				params.Add("items", id)
			}
			v.Suggestions = append(v.Suggestions, TimeSuggestion{s.Day, s.StartTime, s.Period, "/booking/" + b.ID + "?" + params.Encode()})
			if len(v.Suggestions) == 3 {
				break
			}
		}
	}
	return v
}
func (a *App) exactBookingStep(w http.ResponseWriter, r *http.Request, b Booking) {
	b.Items = append([]string{}, r.Form["items"]...)
	parts := strings.Split(r.FormValue("slot"), "|")
	if len(parts) != 2 {
		a.fail(w, r, "یک ساعت آزاد انتخاب کنید", 400)
		return
	}
	chosen, e := time.Parse(time.RFC3339, normalize(parts[1]))
	if e != nil {
		a.fail(w, r, "ساعت انتخابی معتبر نیست", 400)
		return
	}
	// Accept only a registered, eligible window and its 15-minute selection grid.
	quoteBefore, err := a.quote(r.Context(), b)
	if err != nil || quoteBefore.Minutes < 5 || quoteBefore.Minutes > 1440 || !a.enabled(r.Context(), "service", b.Service) || (b.Service != "vet" && !a.enabled(r.Context(), "area", b.Area)) {
		a.fail(w, r, "خدمات یا مدت انتخابی معتبر نیستند", 409)
		return
	}
	var selectedProvider string
	err = a.DB.QueryRow(r.Context(), `SELECT p.id FROM availability a JOIN providers p ON p.id=a.provider_id WHERE a.id=$1 AND p.active AND p.verified AND p.service=$2 AND $3=ANY(p.species) AND ($2='vet' OR $4=ANY(p.areas)) AND ($5='' OR p.id=$5) AND a.day<=(now() AT TIME ZONE 'Asia/Tehran')::date+90 AND mod(extract(epoch FROM ($6::timestamptz-((a.day+a.start_time) AT TIME ZONE 'Asia/Tehran'))),900)=0 AND booking_time_available(p.id,a.id,$6,$7,$8)`, parts[0], b.Service, b.Species, b.Area, b.ProviderID, chosen, quoteBefore.Minutes, b.ID).Scan(&selectedProvider)
	if err != nil {
		a.fail(w, r, "این ساعت دیگر آزاد نیست؛ روز و ساعت دیگری انتخاب کنید", 409)
		return
	}
	slot := &Slot{ID: parts[0], ProviderID: selectedProvider, Start: chosen}
	b.ProviderID = slot.ProviderID
	quote, e := a.quote(r.Context(), b)
	if e != nil {
		a.fail(w, r, "قیمت یا مدت خدمت معتبر نیست", 409)
		return
	}
	_, e = a.DB.Exec(r.Context(), "UPDATE bookings SET provider_id=$2,availability_id=$3,items=$4,amount=$5,pricing_snapshot=$6,exact_time=$7,duration_minutes=$8,lab_cost=COALESCE(($6::jsonb->'Lab'->>'Cost')::bigint,0),stage=4 WHERE id=$1 AND status='draft'", b.ID, b.ProviderID, slot.ID, b.Items, quote.Total, mustJSON(quote), slot.Start, quote.Minutes)
	if e != nil {
		a.fail(w, r, "ثبت زمان انجام نشد", 500)
		return
	}
	redirect(w, r, "/booking/"+b.ID)
}
