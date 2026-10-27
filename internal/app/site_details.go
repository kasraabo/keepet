package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type SocialLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}
type SiteDetails struct {
	Revision int64        `json:"-"`
	Phone    string       `json:"phone"`
	Address  string       `json:"address"`
	Socials  []SocialLink `json:"socials"`
}

func (d *SiteDetails) Dial() string { return strings.NewReplacer(" ", "", "-", "").Replace(d.Phone) }

type InterfaceSettings struct {
	Revision int64    `json:"-"`
	Enabled  bool     `json:"enabled"`
	Fields   []string `json:"fields"`
}
type PersianField struct{ ID, Label string }

var persianFields = []PersianField{{"full_name", "نام کامل مشتری"}, {"name", "نام و عنوان"}, {"pet_name", "نام پت"}, {"address", "نشانی"}, {"bio", "معرفی متخصص"}, {"body", "توضیحات و متن"}, {"description", "توضیح کوتاه و خلاصه"}, {"subject", "موضوع متنی تیکت"}, {"q", "جست‌وجو"}, {"diagnosis", "تشخیص پزشک"}, {"symptoms", "علائم پرونده"}, {"prescription", "تجویز پزشک"}}

func defaultInterface() InterfaceSettings {
	return InterfaceSettings{Enabled: true, Fields: []string{"full_name", "name", "pet_name", "address", "bio", "body", "description"}}
}

var protectedPersianName = regexp.MustCompile(`(?i)(email|password|secret|token|phone|mobile|iban|sheba|account|bank|card|amount|price|balance|rate|tracking|reference|transaction|code|url|link|revision|slug|blocks|questions|license)`)

func protectedPersianField(id string) bool {
	return protectedPersianName.MatchString(id) || id == "id" || strings.HasSuffix(id, "_id") || contains([]string{"csrf", "next", "age", "birth_year"}, id)
}

func (a *App) seedSiteDetails(ctx context.Context) error {
	if _, e := a.DB.Exec(ctx, `INSERT INTO business_config(kind,id,data) VALUES('site_details','contacts',$1),('interface','persian',$2) ON CONFLICT DO NOTHING`, mustJSON(SiteDetails{}), mustJSON(defaultInterface())); e != nil {
		return e
	}
	return nil
}
func (a *App) loadSiteDetails(ctx context.Context) *SiteDetails {
	var d SiteDetails
	var raw []byte
	if a.DB.QueryRow(ctx, "SELECT revision,data FROM business_config WHERE kind='site_details' AND id='contacts'").Scan(&d.Revision, &raw) == nil {
		json.Unmarshal(raw, &d)
	}
	return &d
}
func (a *App) loadInterface(ctx context.Context) InterfaceSettings {
	d := defaultInterface()
	var raw []byte
	if a.DB.QueryRow(ctx, "SELECT revision,data FROM business_config WHERE kind='interface' AND id='persian'").Scan(&d.Revision, &raw) == nil {
		json.Unmarshal(raw, &d)
	}
	return d
}
func (a *App) interfaceConfig(w http.ResponseWriter, r *http.Request) {
	d := a.loadInterface(r.Context())
	chatJSON(w, 200, d)
}
func (a *App) siteDetailsAdmin(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	p := a.page(r, "اطلاعات سایت و ورودی فارسی")
	d := a.loadInterface(r.Context())
	p.Interface = &d
	p.Next = ""
	a.render(w, r, "site-details-admin", p)
}

var contactPhone = regexp.MustCompile(`^\+?[0-9][0-9 -]{5,24}$`)

func (a *App) saveSiteSetting(w http.ResponseWriter, r *http.Request, kind, id string, value any) {
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "تنظیم ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	var rev int64
	var before []byte
	e = tx.QueryRow(ctx, "SELECT revision,data FROM business_config WHERE kind=$1 AND id=$2 FOR UPDATE", kind, id).Scan(&rev, &before)
	submitted, err := strconv.ParseInt(normalize(r.FormValue("revision")), 10, 64)
	if e != nil || err != nil || submitted != rev {
		a.fail(w, r, "نسخهٔ تنظیم تغییر کرده؛ صفحه را تازه کنید", 409)
		return
	}
	after := mustJSON(value)
	_, e = tx.Exec(ctx, "UPDATE business_config SET data=$3,revision=revision+1 WHERE kind=$1 AND id=$2", kind, id, after)
	if e == nil {
		_, e = tx.Exec(ctx, "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,$2,$3,$4,$5)", sess(r).User.ID, kind, id, before, after)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "تنظیم ذخیره نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/management/site-details", "تنظیمات ذخیره شدند."))
}
func (a *App) saveSiteDetails(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	d := SiteDetails{Phone: normalize(r.FormValue("phone")), Address: trimmed(r.FormValue("address"))}
	if (d.Phone != "" && !contactPhone.MatchString(d.Phone)) || len(d.Address) > 2000 {
		a.fail(w, r, "شماره تماس یا نشانی معتبر نیست", 400)
		return
	}
	labels, links := r.Form["social_label"], r.Form["social_url"]
	if len(labels) != len(links) || len(labels) > 6 {
		a.fail(w, r, "حداکثر شش شبکهٔ اجتماعی قابل تنظیم است", 400)
		return
	}
	for i, raw := range links {
		label := trimmed(labels[i])
		raw = trimmed(raw)
		if raw == "" && label == "" {
			continue
		}
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Port() != "" || len(raw) > 500 || label == "" || len(label) > 100 {
			a.fail(w, r, fmt.Sprintf("عنوان و لینک امن شبکهٔ اجتماعی %s را تکمیل کنید", digits(strconv.Itoa(i+1))), 400)
			return
		}
		d.Socials = append(d.Socials, SocialLink{label, raw})
	}
	a.saveSiteSetting(w, r, "site_details", "contacts", d)
}
func (a *App) saveInterface(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	d := InterfaceSettings{Enabled: r.FormValue("enabled") == "on"}
	seen := map[string]bool{}
	for _, id := range r.Form["fields"] {
		found := false
		for _, field := range persianFields {
			if field.ID == id {
				found = true
			}
		}
		if !found || protectedPersianField(id) {
			a.fail(w, r, "این ورودی برای تبدیل فارسی مجاز نیست", 400)
			return
		}
		if !seen[id] {
			d.Fields = append(d.Fields, id)
			seen[id] = true
		}
	}
	a.saveSiteSetting(w, r, "interface", "persian", d)
}
func socialSlots(d *SiteDetails) []SocialLink {
	out := make([]SocialLink, 6)
	copy(out, d.Socials)
	return out
}
