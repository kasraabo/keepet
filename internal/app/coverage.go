package app

import (
	"encoding/json"
	"net/http"
	"strconv"

	assets "keepet"
)

type DistrictBoundary struct {
	ID, Name, Path string
	Number         int
	X, Y           float64
}
type CoverageDistrict struct {
	DistrictBoundary
	Enabled  bool
	Revision int64
}

var DistrictBoundaries = loadDistrictBoundaries()

func loadDistrictBoundaries() []DistrictBoundary {
	raw, e := assets.Files.ReadFile("config/tehran-district-paths.json")
	if e != nil {
		panic(e)
	}
	var out []DistrictBoundary
	if e = json.Unmarshal(raw, &out); e != nil {
		panic(e)
	}
	if len(out) != 22 {
		panic("Tehran map must contain all 22 districts")
	}
	for i := range out {
		out[i].Name = digits(out[i].Name)
	}
	return out
}
func allAreas() []Area {
	var out []Area
	for _, d := range DistrictBoundaries {
		out = append(out, Area{d.ID, d.Name, d.Path})
	}
	// Preserve existing neighbourhood coverage and historic bookings. These are not district polygons.
	return append(out, Area{"saadat", "سعادت‌آباد", ""}, Area{"shahrak", "شهرک غرب", ""}, Area{"vanak", "ونک", ""}, Area{"yousef", "یوسف‌آباد", ""}, Area{"niavaran", "نیاوران", ""})
}
func (a *App) coveragePage(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	p := a.page(r, "مناطق تحت پوشش تهران")
	rs, e := a.records(r.Context(), "area")
	if e != nil {
		a.fail(w, r, "تنظیمات پوشش بارگذاری نشد", 500)
		return
	}
	for _, rec := range rs {
		if areaName(rec.ID) != "خارج از پوشش" && !isDistrictID(rec.ID) {
			p.LegacyCoverage = append(p.LegacyCoverage, rec)
		}
	}
	a.render(w, r, "coverage-admin", p)
}
func isDistrictID(id string) bool {
	for _, d := range DistrictBoundaries {
		if d.ID == id {
			return true
		}
	}
	return false
}
func (a *App) saveCoverage(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	id := r.PathValue("id")
	if areaName(id) == "خارج از پوشش" {
		a.fail(w, r, "منطقه نامعتبر است", 404)
		return
	}
	rev, e := strconv.ParseInt(normalize(r.FormValue("revision")), 10, 64)
	if e != nil {
		a.fail(w, r, "نسخهٔ تنظیمات نامعتبر است", 400)
		return
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ثبت تنظیمات انجام نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	var raw []byte
	var actual int64
	e = tx.QueryRow(ctx, "SELECT revision,data FROM business_config WHERE kind='area' AND id=$1 FOR UPDATE", id).Scan(&actual, &raw)
	if e != nil {
		a.fail(w, r, "منطقه پیدا نشد", 404)
		return
	}
	if actual != rev {
		a.fail(w, r, "تنظیمات تغییر کرده است؛ صفحه را تازه کنید", 409)
		return
	}
	var d BusinessData
	if e = json.Unmarshal(raw, &d); e != nil {
		a.fail(w, r, "تنظیمات منطقه معتبر نیست", 500)
		return
	}
	d.Enabled = r.FormValue("enabled") == "on"
	after := mustJSON(d)
	_, e = tx.Exec(ctx, "UPDATE business_config SET data=$2,revision=revision+1 WHERE kind='area' AND id=$1", id, after)
	if e == nil {
		_, e = tx.Exec(ctx, "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,'area',$2,$3,$4)", sess(r).User.ID, id, raw, after)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "ذخیرهٔ پوشش انجام نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/management/coverage", "پوشش منطقه به‌روز شد."))
}
