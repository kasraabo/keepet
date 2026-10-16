package app

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

func (p DiscoveryProvider) Facts() string {
	return string(mustJSON(map[string]any{"species": p.Species, "specialty": p.Specialty}))
}
func (a *App) discoveryAdmin(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	p := a.page(r, "مدیریت جست‌وجو و انتخاب متخصص")
	for _, svc := range []string{"vet", "grooming"} {
		d, e := a.discoveryConfig(r.Context(), svc)
		if e != nil {
			a.fail(w, r, "تنظیمات بارگذاری نشد", 500)
			return
		}
		p.DiscoverySettings = append(p.DiscoverySettings, d)
	}
	rows, e := a.DB.Query(r.Context(), `SELECT p.id,p.name,p.service,p.species,COALESCE(f.specialties,'') FROM providers p LEFT JOIN provider_profiles f ON f.provider_id=p.id WHERE p.service IN ('vet','grooming') ORDER BY p.name,p.id`)
	if e != nil {
		a.fail(w, r, "متخصصان بارگذاری نشدند", 500)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var x DiscoveryProvider
		if e = rows.Scan(&x.ID, &x.Name, &x.Service, &x.Species, &x.Specialty); e != nil {
			break
		}
		p.DiscoveryProviders = append(p.DiscoveryProviders, x)
	}
	if e == nil {
		e = rows.Err()
	}
	if e != nil {
		a.fail(w, r, "متخصصان بارگذاری نشدند", 500)
		return
	}
	a.render(w, r, "discovery-admin", p)
}
func (a *App) saveDiscovery(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	svc := r.PathValue("service")
	if !contains([]string{"vet", "grooming"}, svc) {
		a.fail(w, r, "خدمت نامعتبر", 404)
		return
	}
	c := defaultDiscovery(svc)
	c.Search = r.FormValue("search") == "on"
	c.SearchLabel = strings.TrimSpace(r.FormValue("search_label"))
	c.Layout = r.FormValue("layout") == "on"
	c.DefaultLayout = r.FormValue("default_layout")
	c.DefaultSort = r.FormValue("default_sort")
	size, e := strconv.Atoi(normalize(r.FormValue("page_size")))
	revision, e2 := strconv.ParseInt(normalize(r.FormValue("revision")), 10, 64)
	if e != nil || e2 != nil || !contains([]string{"6", "12", "24", "48"}, strconv.Itoa(size)) || !contains([]string{"list", "cards"}, c.DefaultLayout) || len(c.SearchLabel) < 3 || len(c.SearchLabel) > 200 {
		a.fail(w, r, "عنوان جست‌وجو، اندازهٔ صفحه و چیدمان معتبر انتخاب کنید", 400)
		return
	}
	c.PageSize = size
	configure := func(opts []DiscoveryOption, prefix string) bool {
		order := map[string]int{}
		for i := range opts {
			o := &opts[i]
			o.Enabled = contains(r.Form[prefix], o.ID)
			o.Label = strings.TrimSpace(r.FormValue(prefix + "_label_" + o.ID))
			n, err := strconv.Atoi(normalize(r.FormValue(prefix + "_order_" + o.ID)))
			if err != nil || n < 0 || n > 100 || o.Label == "" || len(o.Label) > 200 {
				return false
			}
			order[o.ID] = n
		}
		sort.SliceStable(opts, func(i, j int) bool { return order[opts[i].ID] < order[opts[j].ID] })
		return true
	}
	if !configure(c.Filters, "filters") || !configure(c.Sorts, "sorts") || !optionEnabled(c.Sorts, c.DefaultSort) {
		a.fail(w, r, "عنوان و ترتیب گزینه‌ها لازم است؛ ترتیب پیش‌فرض باید فعال باشد", 400)
		return
	}
	c.Specialties = specialtyTags(r.FormValue("specialties"))
	if len(c.Specialties) > 60 {
		a.fail(w, r, "حداکثر ۶۰ تخصص مجاز است", 400)
		return
	}
	for _, v := range c.Specialties {
		if len(v) > 200 {
			a.fail(w, r, "عنوان تخصص طولانی است", 400)
			return
		}
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	var actual int64
	var before []byte
	e = tx.QueryRow(ctx, "SELECT revision,data FROM business_config WHERE kind='discovery' AND id=$1 FOR UPDATE", svc).Scan(&actual, &before)
	if e != nil {
		a.fail(w, r, "تنظیم پیدا نشد", 404)
		return
	}
	if actual != revision {
		a.fail(w, r, "تنظیمات تغییر کرده‌اند؛ صفحه را تازه کنید", 409)
		return
	}
	after := mustJSON(c)
	_, e = tx.Exec(ctx, "UPDATE business_config SET data=$2,revision=revision+1 WHERE kind='discovery' AND id=$1", svc, after)
	if e == nil {
		_, e = tx.Exec(ctx, "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,'discovery',$2,$3,$4)", sess(r).User.ID, svc, before, after)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "تنظیم ذخیره نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/management/discovery", "تنظیمات جست‌وجو، فیلتر و مرتب‌سازی ذخیره شدند."))
}
func (a *App) saveDiscoveryFacts(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	species := r.Form["species"]
	specialty := strings.TrimSpace(r.FormValue("specialty"))
	if len(species) < 1 || len(species) > 2 || len(specialty) > 2000 {
		a.fail(w, r, "حداقل یک نوع پت و متن تخصص معتبر لازم است", 400)
		return
	}
	seen := map[string]bool{}
	for _, s := range species {
		if !contains([]string{"cat", "dog"}, s) || seen[s] {
			a.fail(w, r, "نوع پت معتبر انتخاب کنید", 400)
			return
		}
		seen[s] = true
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	var p DiscoveryProvider
	e = tx.QueryRow(ctx, "SELECT id,species,service FROM providers WHERE id=$1 FOR UPDATE", r.PathValue("id")).Scan(&p.ID, &p.Species, &p.Service)
	if e != nil || !contains([]string{"vet", "grooming"}, p.Service) {
		a.fail(w, r, "متخصص پیدا نشد", 404)
		return
	}
	e = tx.QueryRow(ctx, "SELECT COALESCE((SELECT specialties FROM provider_profiles WHERE provider_id=$1),'')", p.ID).Scan(&p.Specialty)
	if e != nil {
		a.fail(w, r, "خواندن مشخصات انجام نشد", 500)
		return
	}
	before := p.Facts()
	if r.FormValue("original") != before {
		a.fail(w, r, "مشخصات تغییر کرده؛ صفحه را تازه کنید", 409)
		return
	}
	_, e = tx.Exec(ctx, "UPDATE providers SET species=$2 WHERE id=$1", p.ID, species)
	if e == nil {
		_, e = tx.Exec(ctx, "INSERT INTO provider_profiles(provider_id,specialties) VALUES($1,$2) ON CONFLICT(provider_id) DO UPDATE SET specialties=EXCLUDED.specialties", p.ID, specialty)
	}
	if e == nil {
		after, _ := json.Marshal(map[string]any{"species": species, "specialty": specialty})
		_, e = tx.Exec(ctx, "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,'discovery_profile',$2,$3,$4)", sess(r).User.ID, p.ID, []byte(before), after)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "مشخصات ذخیره نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/management/discovery#facts", "تخصص و نوع پت قابل پذیرش ذخیره شدند."))
}
