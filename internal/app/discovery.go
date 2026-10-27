package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type DiscoveryOption struct {
	ID, Label string
	Enabled   bool
}
type DiscoveryConfig struct {
	Name           string `json:"name"`
	Search         bool   `json:"search"`
	SearchLabel    string `json:"search_label"`
	Layout         bool   `json:"layout"`
	DefaultLayout  string `json:"default_layout"`
	DefaultSort    string `json:"default_sort"`
	PageSize       int    `json:"page_size"`
	Filters, Sorts []DiscoveryOption
	Specialties    []string
}
type DiscoveryRecord struct {
	Service  string
	Revision int64
	Config   DiscoveryConfig
}
type DiscoveryProvider struct {
	Provider
	Specialty  string
	Tags       []string
	Cost       int64
	Rating     float64
	RatingText string
	Reviews    int
	Available  bool
	Next       *time.Time
	NextLabel  string
}
type DiscoveryView struct {
	HasFilters                                                                                               bool
	Config                                                                                                   DiscoveryConfig
	Service, Path, Query, Sort, Layout, Specialty, Species, Area, Day, At, Min, Max, Rating, Selected, Error string
	Available                                                                                                bool
	Booking                                                                                                  bool
	Specialties                                                                                              []string
	Cards                                                                                                    []DiscoveryProvider
	Total, Page                                                                                              int
	NextURL, PrevURL                                                                                         string
}

func defaultDiscovery(svc string) DiscoveryConfig {
	c := DiscoveryConfig{Name: service(svc).Name, Search: true, SearchLabel: "جست‌وجوی نام یا معرفی متخصص", Layout: true, DefaultLayout: "list", DefaultSort: "recommended", PageSize: 12,
		Filters: []DiscoveryOption{{"specialty", "تخصص", true}, {"available", "فقط دارای بازهٔ آزاد", true}, {"day", "روز خدمت", true}, {"at", "ساعت آزاد", true}, {"species", "نوع پت", true}, {"rating", "حداقل امتیاز", true}, {"price", "محدودهٔ هزینه", true}, {"area", "منطقه", true}},
		Sorts:   []DiscoveryOption{{"recommended", "پیشنهادی", true}, {"price_asc", "هزینه؛ کم به زیاد", true}, {"price_desc", "هزینه؛ زیاد به کم", true}, {"rating_desc", "امتیاز؛ زیاد به کم", true}, {"rating_asc", "امتیاز؛ کم به زیاد", true}, {"available", "دارای بازهٔ آزاد ابتدا", true}, {"soonest", "نزدیک‌ترین بازهٔ آزاد", true}, {"experience", "بیشترین سابقه", true}, {"name", "نام؛ الفبایی", true}, {"specialty", "تخصص؛ الفبایی", svc == "vet"}}}
	for i := range c.Filters {
		if c.Filters[i].ID == "specialty" {
			c.Filters[i].Enabled = svc == "vet"
		}
		if c.Filters[i].ID == "area" {
			c.Filters[i].Enabled = svc == "grooming"
		}
	}
	return c
}
func optionEnabled(opts []DiscoveryOption, id string) bool {
	for _, o := range opts {
		if o.ID == id {
			return o.Enabled
		}
	}
	return false
}
func (a *App) seedDiscovery(ctx context.Context) error {
	for _, svc := range []string{"vet", "grooming"} {
		_, e := a.DB.Exec(ctx, "INSERT INTO business_config(kind,id,data) VALUES('discovery',$1,$2) ON CONFLICT DO NOTHING", svc, mustJSON(defaultDiscovery(svc)))
		if e != nil {
			return e
		}
	}
	return nil
}
func (a *App) discoveryConfig(ctx context.Context, svc string) (DiscoveryRecord, error) {
	d := DiscoveryRecord{Service: svc, Config: defaultDiscovery(svc)}
	var raw []byte
	e := a.DB.QueryRow(ctx, "SELECT revision,data FROM business_config WHERE kind='discovery' AND id=$1", svc).Scan(&d.Revision, &raw)
	if e != nil {
		return d, e
	}
	e = json.Unmarshal(raw, &d.Config)
	for _, o := range defaultDiscovery(svc).Sorts {
		found := false
		for _, old := range d.Config.Sorts {
			if old.ID == o.ID {
				found = true
			}
		}
		if !found {
			d.Config.Sorts = append(d.Config.Sorts, o)
		}
	}
	return d, e
}
func searchText(s string) string {
	s = strings.NewReplacer("ي", "ی", "ك", "ک", "\u200c", " ").Replace(strings.ToLower(normalize(s)))
	s = strings.Map(func(c rune) rune {
		if unicode.Is(unicode.Mn, c) {
			return -1
		}
		return c
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
func specialtyTags(s string) []string {
	parts := strings.FieldsFunc(s, func(c rune) bool { return c == ',' || c == '،' || c == ';' || c == '؛' || c == '\n' })
	out := []string{}
	seen := map[string]bool{}
	for _, x := range parts {
		x = strings.TrimSpace(x)
		key := searchText(x)
		if key != "" && !seen[key] {
			seen[key] = true
			out = append(out, x)
		}
	}
	return out
}
func (a *App) discover(r *http.Request, b *Booking) (*DiscoveryView, error) {
	q := r.URL.Query()
	svc := q.Get("service")
	if b != nil {
		svc = b.Service
	}
	if !contains([]string{"vet", "grooming"}, svc) {
		svc = "vet"
	}
	record, e := a.discoveryConfig(r.Context(), svc)
	if e != nil {
		return nil, e
	}
	c := record.Config
	v := &DiscoveryView{Config: c, Service: svc, Path: r.URL.Path, Sort: c.DefaultSort, Layout: c.DefaultLayout, Page: 1, Booking: b != nil}
	if !a.enabled(r.Context(), "service", svc) {
		v.Error = "این خدمت فعلاً فعال نیست."
		return v, nil
	}
	for _, f := range c.Filters {
		if f.Enabled {
			v.HasFilters = true
		}
	}
	allowed := func(key string) string {
		if optionEnabled(c.Filters, key) {
			return strings.TrimSpace(q.Get(key))
		}
		return ""
	}
	if c.Search {
		v.Query = strings.TrimSpace(q.Get("q"))
		if len(v.Query) > 400 {
			v.Query = string([]rune(v.Query)[:min(100, len([]rune(v.Query)))])
		}
	}
	if optionEnabled(c.Sorts, q.Get("sort")) {
		v.Sort = q.Get("sort")
	}
	if c.Layout && contains([]string{"list", "cards"}, q.Get("layout")) {
		v.Layout = q.Get("layout")
	}
	v.Specialty = allowed("specialty")
	if v.Specialty != "" && len(c.Specialties) > 0 {
		allowedTag := false
		for _, tag := range c.Specialties {
			if searchText(tag) == searchText(v.Specialty) {
				allowedTag = true
			}
		}
		if !allowedTag {
			v.Specialty = ""
		}
	}
	v.Species = allowed("species")
	v.Area = allowed("area")
	v.Day = allowed("day")
	v.At = normalize(allowed("at"))
	v.Available = allowed("available") == "1"
	v.Rating = allowed("rating")
	if optionEnabled(c.Filters, "price") {
		v.Min = strings.TrimSpace(q.Get("min_price"))
		v.Max = strings.TrimSpace(q.Get("max_price"))
	}
	if n, err := strconv.Atoi(normalize(q.Get("page"))); err == nil && n > 0 && n <= 10000 {
		v.Page = n
	}
	if b != nil {
		v.Selected = b.ProviderID
	}
	if q.Has("provider") {
		v.Selected = q.Get("provider")
	}
	var lower, upper int64
	upper = 1000000000
	for key, value := range map[string]string{"min": v.Min, "max": v.Max} {
		if value != "" {
			n, err := strconv.ParseInt(normalizeNumber(value), 10, 64)
			if err != nil || n < 0 || n > 1000000000 {
				v.Error = "محدودهٔ هزینه را با عدد صحیح تومان وارد کنید."
				return v, nil
			}
			if key == "min" {
				lower = n
			} else {
				upper = n
			}
		}
	}
	if upper < lower {
		v.Error = "حداکثر هزینه باید برابر یا بیشتر از حداقل باشد."
		return v, nil
	}
	minRating := 0.0
	if v.Rating != "" {
		minRating, e = strconv.ParseFloat(normalize(v.Rating), 64)
		if e != nil || minRating < 0 || minRating > 5 {
			v.Error = "امتیاز باید بین صفر و پنج باشد."
			return v, nil
		}
	}
	if v.Species != "" && !contains([]string{"cat", "dog"}, v.Species) {
		v.Error = "نوع پت معتبر انتخاب کنید."
		return v, nil
	}
	if v.Area != "" && !a.enabled(r.Context(), "area", v.Area) {
		v.Error = "منطقهٔ تحت پوشش انتخاب کنید."
		return v, nil
	}
	var day string
	if v.Day != "" {
		d, err := parseCalendar(v.Day, a.Location)
		if err != nil {
			v.Error = "روز خدمت را از تقویم شمسی انتخاب کنید."
			return v, nil
		}
		day = d.Format("2006-01-02")
		v.Day = day
	}
	if v.At != "" {
		if _, err := time.Parse("15:04", v.At); err != nil {
			v.Error = "ساعت آزاد را به صورت ساعت و دقیقه وارد کنید."
			return v, nil
		}
	}
	ps, e := a.providers(r.Context(), svc)
	if e != nil {
		return nil, e
	}
	// Load metadata, published ratings and all free windows in batches; no synthetic ratings.
	meta := map[string]string{}
	rows, e := a.DB.Query(r.Context(), "SELECT p.id,COALESCE(f.specialties,'') FROM providers p LEFT JOIN provider_profiles f ON f.provider_id=p.id WHERE p.service=$1", svc)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id, s string
		if e = rows.Scan(&id, &s); e != nil {
			break
		}
		meta[id] = s
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return nil, e
	}
	type stars struct {
		Avg   float64
		Count int
	}
	ratings := map[string]stars{}
	rows, e = a.DB.Query(r.Context(), `SELECT provider_id,avg(rating)::float8,count(*) FROM (
 SELECT b.provider_id,rv.rating FROM reviews rv JOIN bookings b ON b.id=rv.booking_id WHERE rv.published AND b.service<>'vet'
 UNION ALL SELECT p.id,f.rating FROM consultation_customer_forms f JOIN consultation_chats c ON c.id=f.chat_id JOIN providers p ON p.user_id=c.vet_id WHERE f.published
 ) x GROUP BY provider_id`)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id string
		var x stars
		if e = rows.Scan(&id, &x.Avg, &x.Count); e != nil {
			break
		}
		ratings[id] = x
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return nil, e
	}
	windows := map[string][]Slot{}
	now := time.Now()
	rows, e = a.DB.Query(r.Context(), `SELECT a.id,a.provider_id,a.day::text,a.period,to_char(a.start_time,'HH24:MI'),to_char(a.end_time,'HH24:MI'),a.capacity FROM availability a JOIN providers p ON p.id=a.provider_id WHERE p.service=$1 AND p.active AND p.verified AND a.day>=(now() AT TIME ZONE 'Asia/Tehran')::date AND a.day<(now() AT TIME ZONE 'Asia/Tehran')::date+interval '14 days' AND booking_window_has_time(a.id,CASE WHEN $1='vet' THEN COALESCE(NULLIF((SELECT (data->>'visit_minutes')::integer FROM business_config WHERE kind='service' AND id='vet'),0),30) WHEN $1='sampling' THEN COALESCE(NULLIF((SELECT (data->>'duration_minutes')::integer FROM business_config WHERE kind='service' AND id='sampling'),0),30) ELSE COALESCE((SELECT min(COALESCE(t.duration_minutes,NULLIF((c.data->>'duration_minutes')::integer,0),30)) FROM business_config c LEFT JOIN provider_tariffs t ON t.provider_id=p.id AND t.service_id=c.id WHERE c.kind='grooming' AND (c.data->>'enabled')::boolean AND COALESCE(t.enabled,NOT COALESCE((c.data->>'provider_priced')::boolean,false))),30) END,'') ORDER BY a.day,a.start_time,a.id`, svc)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var x Slot
		if e = rows.Scan(&x.ID, &x.ProviderID, &x.Day, &x.Period, &x.StartTime, &x.EndTime, &x.Capacity); e != nil {
			break
		}
		a.slotTimes(&x)
		if x.End.After(now) {
			windows[x.ProviderID] = append(windows[x.ProviderID], x)
		}
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return nil, e
	}
	specialties := map[string]string{}
	for _, p := range ps {
		if !p.Verified {
			continue
		}
		if b != nil && (!contains(p.Species, b.Species) || (svc != "vet" && !contains(p.Areas, b.Area))) {
			continue
		}
		card := DiscoveryProvider{Provider: p, Specialty: meta[p.ID], Tags: specialtyTags(meta[p.ID]), Cost: p.Price}
		if svc == "vet" {
			q, err := a.quote(r.Context(), Booking{Service: svc, ProviderID: p.ID})
			if err != nil {
				return nil, err
			}
			card.Cost = q.Total
		} else {
			menu, err := a.groomTariffs(r.Context(), p.ID)
			if err != nil {
				return nil, err
			}
			card.Cost = 0
			for _, m := range menu {

				if m.Enabled {
					q, err := a.quote(r.Context(), Booking{Service: svc, ProviderID: p.ID, Items: []string{m.ID}})
					if err != nil {
						return nil, err
					}
					if card.Cost == 0 || q.Total < card.Cost {
						card.Cost = q.Total
					}
				}
			}
			if b != nil && len(b.Items) > 0 {
				q, err := a.quote(r.Context(), Booking{Service: svc, ProviderID: p.ID, Items: b.Items})
				if err != nil {
					continue
				}
				card.Cost = q.Total
			}
			if card.Cost == 0 {
				continue
			}
		}
		for _, tag := range card.Tags {
			specialties[searchText(tag)] = tag
		}
		r := ratings[p.ID]
		card.Rating = r.Avg
		card.Reviews = r.Count
		if r.Count > 0 {
			card.RatingText = digits(fmt.Sprintf("%.1f", r.Avg))
		}
		for _, slot := range windows[p.ID] {
			if day != "" && slot.Day != day {
				continue
			}
			if v.At != "" {
				t, err := time.ParseInLocation("2006-01-02T15:04", slot.Day+"T"+v.At, a.Location)
				if err != nil || t.Before(slot.Start) || !t.Before(slot.End) || !t.After(now) {
					continue
				}
			}
			card.Available = true
			at := slot.Start
			if at.Before(now) {
				at = now
			}
			card.Next = &at
			card.NextLabel = slot.Label()
			break
		}
		text := searchText(p.Name + " " + p.Bio + " " + card.Specialty)
		if v.Query != "" && !strings.Contains(text, searchText(v.Query)) {
			continue
		}
		if v.Specialty != "" {
			found := false
			for _, tag := range card.Tags {
				if searchText(tag) == searchText(v.Specialty) {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		if v.Species != "" && !contains(p.Species, v.Species) {
			continue
		}
		if v.Area != "" && !contains(p.Areas, v.Area) {
			continue
		}
		if (v.Available || day != "" || v.At != "") && !card.Available {
			continue
		}
		if card.Cost < lower || card.Cost > upper || minRating > 0 && (card.Reviews == 0 || card.Rating < minRating) {
			continue
		}
		v.Cards = append(v.Cards, card)
	}
	if len(c.Specialties) > 0 {
		v.Specialties = c.Specialties
	} else {
		for _, tag := range specialties {
			v.Specialties = append(v.Specialties, tag)
		}
		sort.Strings(v.Specialties)
	}
	sort.SliceStable(v.Cards, func(i, j int) bool {
		a, b := v.Cards[i], v.Cards[j]
		switch v.Sort {
		case "price_asc":
			if a.Cost != b.Cost {
				return a.Cost < b.Cost
			}
		case "price_desc":
			if a.Cost != b.Cost {
				return a.Cost > b.Cost
			}
		case "rating_asc", "rating_desc":
			if (a.Reviews == 0) != (b.Reviews == 0) {
				return a.Reviews > 0
			}
			if a.Rating != b.Rating {
				if v.Sort == "rating_asc" {
					return a.Rating < b.Rating
				}
				return a.Rating > b.Rating
			}
		case "available", "soonest":
			if a.Available != b.Available {
				return a.Available
			}
			if v.Sort == "soonest" && a.Next != nil && b.Next != nil && !a.Next.Equal(*b.Next) {
				return a.Next.Before(*b.Next)
			}
		case "experience":
			if a.Experience != b.Experience {
				return a.Experience > b.Experience
			}
		case "specialty":
			x, y := searchText(a.Specialty), searchText(b.Specialty)
			if (x == "") != (y == "") {
				return x != ""
			}
			if x != y {
				return x < y
			}
		case "name":
			if a.Name != b.Name {
				return a.Name < b.Name
			}
		default:
			if a.Available != b.Available {
				return a.Available
			}
			if a.Rating != b.Rating {
				return a.Rating > b.Rating
			}
			if a.Experience != b.Experience {
				return a.Experience > b.Experience
			}
		}
		return a.ID < b.ID
	})
	v.Total = len(v.Cards)
	size := c.PageSize
	if size < 1 || size > 48 {
		size = 12
	}
	maxPage := max(1, (v.Total+size-1)/size)
	if v.Page > maxPage {
		v.Page = maxPage
	}
	link := func(page int) string {
		qs := r.URL.Query()
		qs.Set("page", strconv.Itoa(page))
		return r.URL.Path + "?" + qs.Encode()
	}
	if v.Page > 1 {
		v.PrevURL = link(v.Page - 1)
	}
	if v.Page < maxPage {
		v.NextURL = link(v.Page + 1)
	}
	start := (v.Page - 1) * size
	v.Cards = v.Cards[start:min(start+size, v.Total)]
	return v, nil
}
func (a *App) providerDirectory(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "پیداکردن متخصص")
	v, e := a.discover(r, nil)
	if e != nil {
		a.fail(w, r, "فهرست متخصصان بارگذاری نشد؛ دوباره تلاش کنید.", 503)
		return
	}
	p.Discovery = v
	if r.Header.Get("HX-Request") == "true" {
		a.renderFragment(w, r, "provider-results", "#provider-results", p)
		return
	}
	a.render(w, r, "provider-directory", p)
}
func (a *App) renderFragment(w http.ResponseWriter, r *http.Request, name, target string, p Page) {
	var b strings.Builder
	if e := a.Templates.ExecuteTemplate(&b, name, p); e != nil {
		a.fail(w, r, "بارگذاری بخش انجام نشد", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("HX-Retarget", target)
	w.Header().Set("HX-Reswap", "outerHTML")
	fmt.Fprint(w, b.String())
}
