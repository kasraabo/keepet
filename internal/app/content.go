package app

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type SEOData struct{ Title, Description, Canonical string }

func (a *App) applySEO(r *http.Request, p *Page) {
	rs, _ := a.records(r.Context(), "seo")
	p.SEO.Description = "کیپت؛ مشاوره آنلاین دامپزشکی، گرومینگ و نمونه‌برداری در منزل در تهران، با هماهنگی تیم مراقبت."
	for _, rec := range rs {
		if rec.Data.Enabled && rec.Data.Link == editorialSEOPath(r) {
			p.SEO.Title = rec.Data.Name
			p.SEO.Description = rec.Data.Description
			p.SEOBody = rec.Data.Body
			d := rec.Data
			p.SEORich = &d
			break
		}
	}
	base := a.publicBase(r.Context())
	if base != "" {
		p.SEO.Canonical = base + editorialSEOPath(r)
	}
}
func (a *App) supportAdmin(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	p := a.page(r, "مدیریت سؤالات متداول")
	p.FAQs, _ = a.records(r.Context(), "faq")
	p.Next = "faq-" + token()[:8]
	a.render(w, r, "support-admin", p)
}
func (a *App) contentAdmin(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin", "content") {
		return
	}
	p := a.page(r, "پنل محتوا و سئو")
	p.Business = &BusinessPage{}
	for _, kind := range []string{"site", "seo", "page", "article"} {
		rs, _ := a.records(r.Context(), kind)
		p.Business.Records = append(p.Business.Records, rs...)
	}
	p.Next = "content-" + token()[:8]
	a.render(w, r, "content-admin", p)
}
func (a *App) saveContent(w http.ResponseWriter, r *http.Request) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	if kind == "faq" {
		if !a.auth(w, r, "admin") {
			return
		}
	} else {
		if !a.auth(w, r, "admin", "content") {
			return
		}
	}
	if !contains([]string{"site", "faq", "seo", "page", "article"}, kind) || !businessID.MatchString(id) {
		a.fail(w, r, "محتوا نامعتبر", 400)
		return
	}
	if contains([]string{"article", "page", "seo"}, kind) {
		a.saveEditorial(w, r)
		return
	}
	d := BusinessData{Name: trimmed(r.FormValue("name")), Body: trimmed(r.FormValue("body")), Link: trimmed(r.FormValue("link")), Description: trimmed(r.FormValue("description")), Enabled: r.FormValue("enabled") == "on"}
	if d.Name == "" || len(d.Name) > 300 || len(d.Body) > 50000 || len(d.Description) > 600 || kind == "seo" && (!validInternalLink(d.Link) || strings.Contains(d.Link, "?")) {
		a.fail(w, r, "عنوان، متن یا مسیر نامعتبر", 400)
		return
	}
	if kind == "seo" && (strings.HasPrefix(d.Link, "/management") || strings.HasPrefix(d.Link, "/content") || strings.HasPrefix(d.Link, "/booking") || strings.HasPrefix(d.Link, "/chat") || strings.HasPrefix(d.Link, "/provider") || d.Link == "/login" || d.Link == "/dashboard") {
		a.fail(w, r, "سئو فقط برای صفحات عمومی قابل تنظیم است", 400)
		return
	}
	if kind == "site" {
		u, e := url.Parse(d.Link)
		if id != "base" || e != nil || u.Host == "" || !contains([]string{"http", "https"}, u.Scheme) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
			a.fail(w, r, "نشانی اصلی باید مانند http://IP:8080 یا https://example.com باشد", 400)
			return
		}
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(r.Context())
	_, e = tx.Exec(r.Context(), "INSERT INTO business_config(kind,id,data) VALUES($1,$2,'{}') ON CONFLICT DO NOTHING", kind, id)
	var before []byte
	var rev int64
	if e == nil {
		e = tx.QueryRow(r.Context(), "SELECT data,revision FROM business_config WHERE kind=$1 AND id=$2 FOR UPDATE", kind, id).Scan(&before, &rev)
	}
	expected, _ := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	if expected == 0 && string(before) == "{}" {
		expected = rev
	}
	if e != nil || rev != expected {
		a.fail(w, r, "نسخهٔ محتوا تغییر کرده؛ صفحه را تازه کنید", 409)
		return
	}
	after := mustJSON(d)
	_, e = tx.Exec(r.Context(), "UPDATE business_config SET data=$3,revision=revision+1 WHERE kind=$1 AND id=$2", kind, id, after)
	if e == nil {
		_, e = tx.Exec(r.Context(), "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,$2,$3,$4,$5)", sess(r).User.ID, kind, id, before, after)
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	next := "/content"
	if kind == "faq" {
		next = "/management/support-content"
	}
	redirect(w, r, noticeURL(next, "محتوا ذخیره شد"))
}
func (a *App) robots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, "User-agent: *\nDisallow: /management\nDisallow: /content\nDisallow: /provider\nDisallow: /dashboard\nDisallow: /booking\nDisallow: /chat\nDisallow: /login\nDisallow: /profile\n")
}
func (a *App) sitemap(w http.ResponseWriter, r *http.Request) {
	type URL struct {
		Loc string `xml:"loc"`
	}
	type Set struct {
		XMLName xml.Name `xml:"urlset"`
		NS      string   `xml:"xmlns,attr"`
		URLs    []URL    `xml:"url"`
	}
	out := Set{NS: "http://www.sitemaps.org/schemas/sitemap/0.9"} // The canonical host comes from configuration, never the request Host header.
	base := a.publicBase(r.Context())
	for _, path := range []string{"/", "/services/vet", "/services/grooming", "/services/sampling", "/support", "/join", "/pages/about", "/pages/magazine", "/specialists?service=vet", "/specialists?service=grooming"} {
		out.URLs = append(out.URLs, URL{base + path})
	}
	ps, _ := a.providers(r.Context(), "")
	for _, p := range ps {
		if p.Verified && p.Service != "sampling" {
			out.URLs = append(out.URLs, URL{base + "/specialists/" + p.ID})
		}
	}
	for _, kind := range []string{"page", "article"} {
		rs, _ := a.records(r.Context(), kind)
		for _, v := range rs {
			if v.Data.Enabled {
				prefix := "/pages/"
				if kind == "article" {
					prefix = "/magazine/"
				}
				if kind == "page" && v.Data.ParentService != "" {
					out.URLs = append(out.URLs, URL{base + "/services/" + v.Data.ParentService + "/" + v.ID})
				} else {
					out.URLs = append(out.URLs, URL{base + prefix + v.ID})
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	xml.NewEncoder(w).Encode(out)
}
func (a *App) customPage(r *http.Request, slug string) (BusinessData, bool) {
	var raw []byte
	if a.DB.QueryRow(r.Context(), "SELECT data FROM business_config WHERE kind='page' AND id=$1 AND (data->>'enabled')::boolean", slug).Scan(&raw) != nil {
		return BusinessData{}, false
	}
	var d BusinessData
	if json.Unmarshal(raw, &d) != nil {
		return d, false
	}
	return d, true
}

func (a *App) seedEditorial(ctx context.Context) error {
	_, e := a.DB.Exec(ctx, "INSERT INTO users(id,phone,name,role) VALUES('demo-content','09120000007','تولید محتوا','content') ON CONFLICT DO NOTHING")
	if e != nil {
		return e
	}
	entries := []BusinessRecord{{Kind: "site", ID: "base", Data: BusinessData{Name: "نشانی عمومی سایت", Link: "http://85.198.54.32:8080", Enabled: true}}, {Kind: "seo", ID: "home", Data: BusinessData{Name: "خدمات دامپزشکی و مراقبت پت در تهران", Description: "مشاوره دامپزشکی آنلاین، گرومینگ و نمونه‌برداری ساده در منزل؛ رزرو با هماهنگی تیم کیپت.", Link: "/", Body: "کیپت همراه شما برای مراقبت از سگ و گربه در تهران است. درخواست‌های مشاوره، گرومینگ و نمونه‌برداری ساده با بررسی و هماهنگی تیم عملیات انجام می‌شوند.", Enabled: true}}, {Kind: "seo", ID: "support", Data: BusinessData{Name: "پشتیبانی کیپت", Description: "پرسش‌های متداول و ثبت درخواست پشتیبانی برای پیگیری خدمات پت.", Link: "/support", Enabled: true}}, {Kind: "seo", ID: "join", Data: BusinessData{Name: "همکاری دامپزشکان و گرومرها با کیپت", Description: "درخواست همکاری متخصصان مراقبت حیوانات خانگی؛ بررسی صلاحیت و هماهنگی توسط عملیات.", Link: "/join", Enabled: true}}}
	for _, svc := range []string{"vet", "grooming"} {
		entries = append(entries, BusinessRecord{Kind: "seo", ID: "directory-" + svc, Data: BusinessData{Name: "انتخاب متخصص کیپت", Link: "/specialists?service=" + svc, Enabled: false}})
	}
	for _, s := range Services {
		entries = append(entries, BusinessRecord{Kind: "seo", ID: s.ID, Data: BusinessData{Name: s.Name + " در تهران", Description: s.Description, Link: "/services/" + s.ID, Body: s.Description + "\nثبت درخواست با بررسی تیم عملیات و هماهنگی زمان دقیق انجام می‌شود. فقط بازه‌های دارای ظرفیت قابل انتخاب هستند.", Enabled: true}})
	}
	for _, slug := range []string{"about", "contact", "magazine", "careers", "terms", "privacy", "refund", "telemedicine"} {
		entries = append(entries, BusinessRecord{Kind: "seo", ID: "page-" + slug, Data: BusinessData{Name: "کیپت", Description: "اطلاعات خدمات، قوانین و مراقبت از حیوانات خانگی در کیپت.", Link: "/pages/" + slug, Enabled: false}})
	}
	for slug, page := range publicPages {
		if slug != "magazine" {
			entries = append(entries, BusinessRecord{Kind: "page", ID: slug, Data: BusinessData{Name: page[0], Body: page[1], Enabled: true}})
		}
	}
	for _, rec := range entries {
		if _, e = a.DB.Exec(ctx, "INSERT INTO business_config(kind,id,data) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", rec.Kind, rec.ID, mustJSON(rec.Data)); e != nil {
			return e
		}
	}
	return nil
}
func (a *App) publicBase(ctx context.Context) string {
	var value string
	if a.DB.QueryRow(ctx, "SELECT data->>'link' FROM business_config WHERE kind='site' AND id='base'").Scan(&value) != nil {
		return ""
	}
	return strings.TrimSuffix(value, "/")
}
