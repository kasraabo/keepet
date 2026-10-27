package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

type ServiceCatalogPage struct {
	AllAreas []BusinessRecord

	Groom, Pages []BusinessRecord
	Packages     []MenuItem
	NewService   string
	Record       *BusinessRecord
	Ready        bool
	Coverage     []string
}

func (a *App) servicePageLinks(ctx context.Context, parent string) []BusinessRecord {
	rs, e := a.records(ctx, "page")
	if e != nil {
		return nil
	}
	out := []BusinessRecord{}
	for _, v := range rs {
		if v.Data.ParentService == parent && v.Data.Enabled {
			out = append(out, v)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Data.Position != out[j].Data.Position {
			return out[i].Data.Position < out[j].Data.Position
		}
		return out[i].ID < out[j].ID
	})
	return out
}
func (a *App) serviceCatalog(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	p := a.page(r, "زیرخدمات و صفحات خدمات")
	v := &ServiceCatalogPage{NewService: "groom-" + token()[:10]}
	var e error
	v.Groom, e = a.records(r.Context(), "grooming")
	if e != nil {
		a.fail(w, r, "فهرست خدمات بارگذاری نشد", 500)
		return
	}
	rs, e := a.records(r.Context(), "page")
	if e != nil {
		a.fail(w, r, "صفحات بارگذاری نشدند", 500)
		return
	}
	for _, rec := range rs {
		if rec.Data.ParentService != "" {
			v.Pages = append(v.Pages, rec)
		}
	}
	areas, err := a.records(r.Context(), "area")
	if err != nil {
		a.fail(w, r, "محدوده‌ها بارگذاری نشدند", 500)
		return
	}
	for _, item := range areas {
		if areaName(item.ID) != "خارج از پوشش" {
			v.AllAreas = append(v.AllAreas, item)
		}
	}
	rows, err := a.DB.Query(r.Context(), "SELECT id,name,active FROM lab_packages ORDER BY name,id")
	if err != nil {
		a.fail(w, r, "پکیج‌ها بارگذاری نشدند", 500)
		return
	}
	for rows.Next() {
		var item MenuItem
		var active bool
		if err = rows.Scan(&item.ID, &item.Name, &active); err != nil {
			rows.Close()
			a.fail(w, r, "پکیج‌ها خوانده نشدند", 500)
			return
		}
		if !active {
			item.Name += " (غیرفعال)"
		}
		v.Packages = append(v.Packages, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		a.fail(w, r, "پکیج‌ها خوانده نشدند", 500)
		return
	}
	p.ServiceCatalog = v
	p.Next = "svc-" + token()[:10]
	a.render(w, r, "service-catalog", p)
}
func (a *App) catalogSave(w http.ResponseWriter, r *http.Request, kind, id string, change func(pgx.Tx, BusinessData) (BusinessData, error)) {
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "تنظیم ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,20020))", kind+":"+id)
	if e != nil {
		a.fail(w, r, "تنظیم خوانده نشد", 500)
		return
	}
	var before []byte
	var rev int64
	e = tx.QueryRow(ctx, "SELECT data,revision FROM business_config WHERE kind=$1 AND id=$2 FOR UPDATE", kind, id).Scan(&before, &rev)
	fresh := e == pgx.ErrNoRows
	if e != nil && !fresh {
		a.fail(w, r, "تنظیم خوانده نشد", 500)
		return
	}
	submitted, err := strconv.ParseInt(normalize(r.FormValue("revision")), 10, 64)
	if err != nil || submitted != rev {
		a.fail(w, r, "نسخهٔ تنظیم تغییر کرده؛ صفحه را تازه کنید", 409)
		return
	}
	var old BusinessData
	if !fresh && json.Unmarshal(before, &old) != nil {
		a.fail(w, r, "تنظیم خوانده نشد", 500)
		return
	}
	d, e := change(tx, old)
	if e != nil {
		a.fail(w, r, e.Error(), 400)
		return
	}
	if fresh {
		_, e = tx.Exec(ctx, "INSERT INTO business_config(kind,id,data) VALUES($1,$2,$3)", kind, id, mustJSON(d))
	} else {
		_, e = tx.Exec(ctx, "UPDATE business_config SET data=$3,revision=revision+1 WHERE kind=$1 AND id=$2", kind, id, mustJSON(d))
	}
	if e == nil {
		_, e = tx.Exec(ctx, "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,$2,$3,$4,$5)", sess(r).User.ID, kind, id, before, mustJSON(d))
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "تنظیم ذخیره نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/management/subservices", "تنظیم ذخیره شد؛ رزروهای پرداخت‌شده و قیمت ثبت‌شدهٔ آن‌ها تغییر نمی‌کنند."))
}
func (a *App) saveGroomingService(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	id := r.PathValue("id")
	if !businessID.MatchString(id) {
		a.fail(w, r, "شناسه خدمت نامعتبر است", 400)
		return
	}
	a.catalogSave(w, r, "grooming", id, func(_ pgx.Tx, d BusinessData) (BusinessData, error) {
		fresh := d.Name == ""
		d.Name = trimmed(r.FormValue("name"))
		d.Description = trimmed(r.FormValue("description"))
		d.Enabled = r.FormValue("enabled") == "on"
		n, e := strconv.Atoi(normalize(r.FormValue("duration_minutes")))
		if d.Name == "" || len(d.Name) > 300 || len(d.Description) > 2000 || e != nil || n < 5 || n > 480 {
			return d, fmt.Errorf("نام خدمت و مدت بین ۵ تا ۴۸۰ دقیقه لازم است؛ اگر توضیح طولانی است، آن را کوتاه‌تر کنید")
		}
		d.DurationMinutes = n
		if fresh {
			d.ProviderPriced = true
			d.Price = 0
		}
		return d, nil
	})
}
func (a *App) saveServicePage(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	id := r.PathValue("id")
	if !businessID.MatchString(id) || !strings.HasPrefix(id, "svc-") {
		a.fail(w, r, "شناسه صفحه نامعتبر است", 400)
		return
	}
	a.catalogSave(w, r, "page", id, func(tx pgx.Tx, d BusinessData) (BusinessData, error) {
		d.Name = trimmed(r.FormValue("name"))
		d.Description = trimmed(r.FormValue("description"))
		d.ParentService = r.FormValue("parent_service")
		d.RelatedProduct = r.FormValue("related_product")
		d.Enabled = r.FormValue("enabled") == "on"
		d.Coverage = nil
		pos, e := strconv.Atoi(normalize(r.FormValue("position")))
		if d.Name == "" || len(d.Name) > 300 || len(d.Description) > 600 || service(d.ParentService).ID == "" || e != nil || pos < 0 || pos > 1000 {
			return d, fmt.Errorf("عنوان، خدمت اصلی و ترتیب بین صفر تا هزار را مشخص کنید")
		}
		d.Position = pos
		if _, ok := r.Form["body"]; ok {
			d.Body = trimmed(r.FormValue("body"))
		}
		if len(d.Body) > 50000 {
			return d, fmt.Errorf("متن صفحه بیش از حد طولانی است")
		}
		if d.RelatedProduct != "" {
			var found bool
			if d.ParentService == "grooming" {
				e = tx.QueryRow(r.Context(), "SELECT true FROM business_config WHERE kind='grooming' AND id=$1 FOR SHARE", d.RelatedProduct).Scan(&found)
			} else if d.ParentService == "sampling" {
				e = tx.QueryRow(r.Context(), "SELECT true FROM lab_packages WHERE id=$1 FOR SHARE", d.RelatedProduct).Scan(&found)
			} else {
				return d, fmt.Errorf("صفحهٔ دامپزشکی محصول آزمایشگاهی یا گرومینگ ندارد")
			}
			if e != nil || !found {
				return d, fmt.Errorf("محصول مرتبط با این نوع خدمت پیدا نشد")
			}
		}
		seen := map[string]bool{}
		for _, area := range r.Form["coverage"] {
			if d.ParentService == "vet" {
				continue
			}
			if seen[area] {
				continue
			}
			var yes bool
			e = tx.QueryRow(r.Context(), "SELECT true FROM business_config WHERE kind='area' AND id=$1 FOR SHARE", area).Scan(&yes)
			if e != nil || !yes || areaName(area) == "خارج از پوشش" {
				return d, fmt.Errorf("محدودهٔ مرتبط باید از مناطق ثبت‌شدهٔ سایت انتخاب شود")
			}
			seen[area] = true
			d.Coverage = append(d.Coverage, area)
		}
		return d, nil
	})
}
func (a *App) servicePageRecord(ctx context.Context, id, parent string) (BusinessRecord, error) {
	rec, e := a.editorialRecord(ctx, "page", id)
	if e != nil || !rec.Data.Enabled || rec.Data.ParentService != parent {
		return rec, fmt.Errorf("صفحه پیدا نشد")
	}
	return rec, nil
}
func (a *App) servicePageReady(ctx context.Context, d BusinessData) bool {
	if !a.enabled(ctx, "service", d.ParentService) {
		return false
	}
	if len(d.Coverage) > 0 {
		any := false
		for _, id := range d.Coverage {
			if a.enabled(ctx, "area", id) {
				any = true
			}
		}
		if !any {
			return false
		}
	}
	if d.RelatedProduct == "" {
		return true
	}
	if d.ParentService == "sampling" {
		return a.labPackageAvailable(ctx, d.RelatedProduct)
	}
	if d.ParentService == "grooming" {
		var ready bool
		a.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM business_config c CROSS JOIN providers p LEFT JOIN provider_tariffs t ON t.provider_id=p.id AND t.service_id=c.id WHERE c.kind='grooming' AND c.id=$1 AND c.data->>'enabled'='true' AND p.service='grooming' AND p.active AND p.verified AND COALESCE(t.enabled,NOT COALESCE((c.data->>'provider_priced')::boolean,false)) AND COALESCE(t.price,(c.data->>'price')::bigint)>0)`, d.RelatedProduct).Scan(&ready)
		return ready
	}
	return false
}
func (a *App) serviceDetail(w http.ResponseWriter, r *http.Request) {
	parent, id := r.PathValue("service"), r.PathValue("page")
	rec, e := a.servicePageRecord(r.Context(), id, parent)
	if e != nil {
		a.fail(w, r, "صفحهٔ خدمت پیدا نشد", 404)
		return
	}
	p := a.page(r, rec.Data.Name)
	p.Service = service(parent)
	p.ServiceCatalog = &ServiceCatalogPage{Record: &rec, Ready: a.servicePageReady(r.Context(), rec.Data)}
	for _, area := range rec.Data.Coverage {
		if a.enabled(r.Context(), "area", area) {
			p.ServiceCatalog.Coverage = append(p.ServiceCatalog.Coverage, areaName(area))
		}
	}
	if p.SEORich == nil {
		p.SEO.Title = rec.Data.Name
		if rec.Data.Description != "" {
			p.SEO.Description = rec.Data.Description
		}
	}
	a.render(w, r, "service-detail", p)
}
func (a *App) serviceDraft(r *http.Request, parent string) (map[string]string, []string, string, string, error) {
	answers := map[string]string{}
	items := []string{}
	sample, area := "", ""
	id := r.FormValue("service_page")
	if id == "" {
		return answers, items, sample, area, nil
	}
	rec, e := a.servicePageRecord(r.Context(), id, parent)
	if e != nil || !a.servicePageReady(r.Context(), rec.Data) {
		return nil, nil, "", "", fmt.Errorf("این صفحه یا زیرخدمت فعلاً قابل رزرو نیست")
	}
	answers["_service_page"] = id
	answers["_service_areas"] = string(mustJSON(rec.Data.Coverage))
	if len(rec.Data.Coverage) == 1 {
		area = rec.Data.Coverage[0]
	}
	if parent == "grooming" && rec.Data.RelatedProduct != "" {
		items = append(items, rec.Data.RelatedProduct)
	}
	if parent == "sampling" {
		sample = rec.Data.RelatedProduct
	}
	return answers, items, sample, area, nil
}
func sourceAreaAllowed(answers map[string]string, area string) bool {
	var areas []string
	if json.Unmarshal([]byte(answers["_service_areas"]), &areas) != nil || len(areas) == 0 {
		return true
	}
	return contains(areas, area)
}
