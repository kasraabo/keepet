package app

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type BusinessData struct {
	DurationMinutes int        `json:"duration_minutes,omitempty"`
	ReviewedBy      string     `json:"reviewed_by,omitempty"`
	ReviewedAt      *time.Time `json:"reviewed_at,omitempty"`

	Name       string     `json:"name"`
	Body       string     `json:"body"`
	Link       string     `json:"link"`
	Enabled    bool       `json:"enabled"`
	Price      int64      `json:"price"`
	Commission int        `json:"commission"`
	Questions  []Question `json:"questions,omitempty"`
}
type BusinessRecord struct {
	Kind, ID string
	Revision int64
	Data     BusinessData
}
type BusinessMetric struct {
	Name               string
	Count              int
	Amount, Commission int64
}
type BusinessMoney struct {
	ID, Provider, Service, Status, Reference string
	Amount, Fee, Due                         int64
	Settled                                  bool
}
type BusinessAudit struct {
	Actor, Kind, Target, Before, After string
	At                                 time.Time
}
type BusinessPage struct {
	Integrations []IntegrationView
	Report       *ManagementReport

	Section, Search                                           string
	Records                                                   []BusinessRecord
	AllProviders                                              []Provider
	Users                                                     []User
	Audit                                                     []BusinessAudit
	Money                                                     []BusinessMoney
	Metrics                                                   []BusinessMetric
	Drafts, Paid, Completed, Owners, ActiveProviders, Pending int
	Gross, Refund, Fees, Due, Settled                         int64
	Triage                                                    string
	TriageRevision                                            int64
}

func (a *App) seedBusiness(ctx context.Context) error {
	put := func(kind, id string, d BusinessData) error {
		b, e := json.Marshal(d)
		if e != nil {
			return e
		}
		_, e = a.DB.Exec(ctx, "INSERT INTO business_config(kind,id,data) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", kind, id, b)
		return e
	}
	for _, s := range Services {
		if e := put("service", s.ID, BusinessData{Name: s.Name, Enabled: true}); e != nil {
			return e
		}
	}
	for _, v := range Areas {
		if e := put("area", v.ID, BusinessData{Name: v.Name, Enabled: true}); e != nil {
			return e
		}
	}
	for _, svc := range []string{"grooming", "sampling"} {
		menu := GroomMenu
		if svc == "sampling" {
			menu = Samples
		}
		for _, v := range menu {
			if e := put(svc, v.ID, BusinessData{Name: v.Name, Price: v.Price, Enabled: true}); e != nil {
				return e
			}
		}
	}
	if e := a.seedIntegrations(ctx); e != nil {
		return e
	}
	if e := put("chat", "consultation", BusinessData{Name: "گفتگوی دامپزشکی", DurationMinutes: 30, Enabled: true}); e != nil {
		return e
	}
	return put("triage", "vet", BusinessData{Questions: a.Triage, Enabled: true})
}
func (a *App) records(ctx context.Context, kind string) ([]BusinessRecord, error) {
	rows, e := a.DB.Query(ctx, "SELECT kind,id,revision,data FROM business_config WHERE ($1='' OR kind=$1) ORDER BY kind,id", kind)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []BusinessRecord
	for rows.Next() {
		var v BusinessRecord
		var raw []byte
		if e = rows.Scan(&v.Kind, &v.ID, &v.Revision, &raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &v.Data); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (a *App) enabled(ctx context.Context, kind, id string) bool {
	var yes bool
	e := a.DB.QueryRow(ctx, "SELECT COALESCE((data->>'enabled')::boolean,false) FROM business_config WHERE kind=$1 AND id=$2", kind, id).Scan(&yes)
	return e == nil && yes
}
func (a *App) questions(ctx context.Context) []Question {
	var raw []byte
	if a.DB.QueryRow(ctx, "SELECT data FROM business_config WHERE kind='triage' AND id='vet'").Scan(&raw) != nil {
		return nil
	}
	var d BusinessData
	if json.Unmarshal(raw, &d) != nil {
		return nil
	}
	return d.Questions
}
func (a *App) liveMenu(ctx context.Context, svc string) []MenuItem {
	rs, e := a.records(ctx, svc)
	if e != nil {
		return nil
	}
	var out []MenuItem
	for _, r := range rs {
		if r.Data.Enabled {
			out = append(out, MenuItem{r.ID, r.Data.Name, r.Data.Price})
		}
	}
	return out
}
func (a *App) livePage(ctx context.Context, p *Page) {
	p.Services = nil
	rs, _ := a.records(ctx, "service")
	for _, s := range Services {
		for _, r := range rs {
			if r.ID == s.ID && r.Data.Enabled {
				s.Name = r.Data.Name
				if s.ID == "vet" {
					a.DB.QueryRow(ctx, "SELECT COALESCE(min(price),0) FROM providers WHERE service='vet' AND active AND verified").Scan(&s.Price)
				} else if s.ID == "sampling" {
					a.DB.QueryRow(ctx, "SELECT COALESCE(min((data->>'price')::bigint),0) FROM business_config WHERE kind='sampling' AND (data->>'enabled')::boolean").Scan(&s.Price)
				}
				p.Services = append(p.Services, s)
			}
		}
	}
	p.Areas = nil
	rs, _ = a.records(ctx, "area")
	for _, v := range Areas {
		for _, r := range rs {
			if r.ID == v.ID && r.Data.Enabled {
				v.Name = r.Data.Name
				p.Areas = append(p.Areas, v)
			}
		}
	}
	p.Menu = a.liveMenu(ctx, "grooming")
	p.Samples = a.liveMenu(ctx, "sampling")
	rs, _ = a.records(ctx, "banner")
	for _, v := range rs {
		if v.Data.Enabled {
			p.Banners = append(p.Banners, v)
		}
	}
}
func (a *App) admin(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	section := r.PathValue("section")
	if section == "" {
		section = "overview"
	}
	if !contains([]string{"overview", "catalog", "supply", "finance", "customers", "triage", "content", "audit", "reports", "integrations"}, section) {
		a.fail(w, r, "بخش پیدا نشد", 404)
		return
	}
	p := a.page(r, "مدیریت کسب‌وکار کیپت")
	b := &BusinessPage{Section: section, Search: strings.TrimSpace(r.URL.Query().Get("q"))}
	p.Business = b
	p.Next = "item-" + token()[:12]
	if section == "overview" || section == "reports" {
		var err error
		b.Report, err = a.loadReport(r.Context(), r)
		if err != nil {
			a.fail(w, r, "بازهٔ گزارش نامعتبر است؛ حداکثر یک سال تا امروز انتخاب کنید", 400)
			return
		}
	}
	if section == "integrations" {
		var err error
		b.Integrations, err = a.integrationViews(r.Context())
		if err != nil {
			a.fail(w, r, "خواندن تنظیمات سرویس‌ها ناموفق بود", 500)
			return
		}
	}

	ctx := r.Context()
	var e error
	b.Records, e = a.records(ctx, "")
	if e != nil {
		a.fail(w, r, "خواندن تنظیمات ناموفق بود", 500)
		return
	}
	for _, v := range b.Records {
		if v.Kind == "triage" {
			raw, _ := json.MarshalIndent(v.Data.Questions, "", "  ")
			b.Triage = string(raw)
			b.TriageRevision = v.Revision
		}
	}
	e = a.DB.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='draft'),count(*) FILTER(WHERE paid_at IS NOT NULL),count(*) FILTER(WHERE status IN ('completed','result_ready')),count(*) FILTER(WHERE status IN ('paid','confirmed','replacement_pending','cancel_requested')),COALESCE(sum(amount) FILTER(WHERE paid_at IS NOT NULL),0),COALESCE(sum(refund_amount) FILTER(WHERE refund_status='refunded'),0) FROM bookings`).Scan(&b.Drafts, &b.Paid, &b.Completed, &b.Pending, &b.Gross, &b.Refund)
	if e != nil {
		a.fail(w, r, "خواندن گزارش ناموفق بود", 500)
		return
	}
	a.DB.QueryRow(ctx, "SELECT count(*) FROM users WHERE role='owner'").Scan(&b.Owners)
	a.DB.QueryRow(ctx, "SELECT count(*) FROM providers WHERE active AND verified").Scan(&b.ActiveProviders)
	rows, e := a.DB.Query(ctx, `SELECT service,count(*),COALESCE(sum(amount),0),COALESCE(sum(amount*commission_bps/10000) FILTER(WHERE status IN ('completed','result_ready')),0) FROM bookings WHERE paid_at IS NOT NULL GROUP BY service ORDER BY service`)
	if e != nil {
		a.fail(w, r, "خواندن گزارش ناموفق بود", 500)
		return
	}
	for rows.Next() {
		var v BusinessMetric
		if e = rows.Scan(&v.Name, &v.Count, &v.Amount, &v.Commission); e != nil {
			break
		}
		b.Metrics = append(b.Metrics, v)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		a.fail(w, r, "خواندن گزارش ناموفق بود", 500)
		return
	}
	rows, e = a.DB.Query(ctx, `SELECT b.id,COALESCE(p.name,'بدون ارائه‌دهنده'),b.service,b.status,b.amount,b.amount*b.commission_bps/10000,b.amount-b.amount*b.commission_bps/10000,x.booking_id IS NOT NULL,COALESCE(x.reference,'') FROM bookings b LEFT JOIN providers p ON p.id=b.provider_id LEFT JOIN business_payouts x ON x.booking_id=b.id WHERE b.paid_at IS NOT NULL ORDER BY b.paid_at DESC`)
	if e != nil {
		a.fail(w, r, "خواندن مالی ناموفق بود", 500)
		return
	}
	for rows.Next() {
		var v BusinessMoney
		if e = rows.Scan(&v.ID, &v.Provider, &v.Service, &v.Status, &v.Amount, &v.Fee, &v.Due, &v.Settled, &v.Reference); e != nil {
			break
		}
		b.Money = append(b.Money, v)
		if contains([]string{"completed", "result_ready"}, v.Status) {
			b.Fees += v.Fee
			if v.Settled {
				b.Settled += v.Due
			} else {
				b.Due += v.Due
			}
		}
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		a.fail(w, r, "خواندن مالی ناموفق بود", 500)
		return
	}
	rows, e = a.DB.Query(ctx, "SELECT id,COALESCE(user_id,''),name,service,bio,license,avatar,verified,active,species,areas,price,experience FROM providers ORDER BY name")
	if e != nil {
		a.fail(w, r, "خواندن عرضه ناموفق بود", 500)
		return
	}
	for rows.Next() {
		var v Provider
		if e = rows.Scan(&v.ID, &v.UserID, &v.Name, &v.Service, &v.Bio, &v.License, &v.Avatar, &v.Verified, &v.Active, &v.Species, &v.Areas, &v.Price, &v.Experience); e != nil {
			break
		}
		b.AllProviders = append(b.AllProviders, v)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		a.fail(w, r, "خواندن عرضه ناموفق بود", 500)
		return
	}
	rows, e = a.DB.Query(ctx, "SELECT id,phone,name,role FROM users WHERE ($1='' OR name ILIKE '%'||$1||'%' OR phone LIKE '%'||$1||'%') ORDER BY created_at DESC LIMIT 200", b.Search)
	if e != nil {
		a.fail(w, r, "خواندن مشتریان ناموفق بود", 500)
		return
	}
	for rows.Next() {
		var v User
		if e = rows.Scan(&v.ID, &v.Phone, &v.Name, &v.Role); e != nil {
			break
		}
		b.Users = append(b.Users, v)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		a.fail(w, r, "خواندن مشتریان ناموفق بود", 500)
		return
	}
	rows, e = a.DB.Query(ctx, "SELECT COALESCE(u.name,'حساب حذف‌شده'),a.kind,a.target,COALESCE(a.before_data::text,''),COALESCE(a.after_data::text,''),a.created_at FROM business_audit a LEFT JOIN users u ON u.id=a.actor_id ORDER BY a.id DESC LIMIT 100")
	if e != nil {
		a.fail(w, r, "خواندن تاریخچه ناموفق بود", 500)
		return
	}
	for rows.Next() {
		var v BusinessAudit
		if e = rows.Scan(&v.Actor, &v.Kind, &v.Target, &v.Before, &v.After, &v.At); e != nil {
			break
		}
		b.Audit = append(b.Audit, v)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		a.fail(w, r, "خواندن تاریخچه ناموفق بود", 500)
		return
	}
	a.render(w, r, "business", p)
}

var businessID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,60}$`)

func validateQuestions(qs []Question) error {
	if len(qs) < 1 || len(qs) > 30 {
		return fmt.Errorf("۱ تا ۳۰ پرسش لازم است")
	}
	seen := map[string]bool{}
	symptom := map[string]bool{}
	for _, q := range qs {
		if q.ID == "symptom" {
			for _, v := range q.Options {
				symptom[v.Value] = true
			}
		}
	}
	for _, q := range qs {
		if !businessID.MatchString(q.ID) || seen[q.ID] || strings.TrimSpace(q.Label) == "" || len(q.Label) > 500 || !contains([]string{"select", "textarea"}, q.Type) {
			return fmt.Errorf("شناسه، عنوان یا نوع پرسش نامعتبر است")
		}
		seen[q.ID] = true
		if q.Type == "select" {
			if len(q.Options) < 1 || len(q.Options) > 30 {
				return fmt.Errorf("گزینه‌های انتخابی لازم‌اند")
			}
			opts := map[string]bool{}
			for _, v := range q.Options {
				if !businessID.MatchString(v.Value) || opts[v.Value] || strings.TrimSpace(v.Label) == "" {
					return fmt.Errorf("گزینهٔ نامعتبر یا تکراری")
				}
				opts[v.Value] = true
			}
		}
		if q.When != "" {
			for _, v := range strings.Split(q.When, ",") {
				if !symptom[v] || q.ID == "symptom" {
					return fmt.Errorf("شرط باید به گزینهٔ موضوع اصلی اشاره کند")
				}
			}
		}
	}
	if !seen["symptom"] {
		return fmt.Errorf("پرسش موضوع اصلی با شناسه symptom لازم است")
	}
	return nil
}
func validInternalLink(v string) bool {
	u, e := url.Parse(v)
	return e == nil && strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//") && !strings.ContainsAny(v, "\\\r\n") && u.Host == "" && u.Scheme == "" && !strings.HasPrefix(v, "/admin") && !strings.HasPrefix(v, "/ops")
}
func (a *App) businessSave(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	kind, id := r.PathValue("kind"), r.PathValue("id")
	ctx := r.Context()
	if !contains([]string{"service", "area", "grooming", "sampling", "triage", "provider", "banner", "article", "chat"}, kind) || !businessID.MatchString(id) || kind == "chat" && id != "consultation" {
		a.fail(w, r, "تنظیم نامعتبر", 400)
		return
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	var before, after []byte
	var rev int64
	section := "catalog"
	if kind == "provider" {
		section = "supply"
		var active, verified bool
		var svc string
		var price int64
		var areas []string
		e = tx.QueryRow(ctx, "SELECT active,verified,price,areas,service FROM providers WHERE id=$1 FOR UPDATE", id).Scan(&active, &verified, &price, &areas, &svc)
		if e != nil {
			a.fail(w, r, "ارائه‌دهنده پیدا نشد", 404)
			return
		}
		before, _ = json.Marshal(map[string]any{"active": active, "price": price, "areas": areas})
		if r.FormValue("original") != string(before) {
			a.fail(w, r, "این اطلاعات تغییر کرده؛ صفحه را تازه کنید", 409)
			return
		}
		price, e = strconv.ParseInt(normalize(r.FormValue("price")), 10, 64)
		areas = append([]string{}, r.Form["areas"]...)
		if e != nil || price < 0 || price > 1000000000 || (svc == "vet" && price == 0) || r.FormValue("enabled") == "on" && !verified {
			a.fail(w, r, "قیمت یا تأیید صلاحیت نامعتبر است", 400)
			return
		}
		for _, v := range areas {
			if areaName(v) == "خارج از پوشش" {
				a.fail(w, r, "محله نامعتبر است", 400)
				return
			}
		}
		active = r.FormValue("enabled") == "on"
		after, _ = json.Marshal(map[string]any{"active": active, "price": price, "areas": areas})
		_, e = tx.Exec(ctx, "UPDATE providers SET active=$2,price=$3,areas=$4 WHERE id=$1", id, active, price, areas)
	} else {
		e = tx.QueryRow(ctx, "SELECT data,revision FROM business_config WHERE kind=$1 AND id=$2 FOR UPDATE", kind, id).Scan(&before, &rev)
		newRow := e == pgx.ErrNoRows && (kind == "banner" || kind == "article")
		if e != nil && !newRow {
			a.fail(w, r, "تنظیم پیدا نشد", 404)
			return
		}
		expected, _ := strconv.ParseInt(r.FormValue("revision"), 10, 64)
		if expected != rev {
			a.fail(w, r, "نسخهٔ فرم قدیمی است؛ صفحه را تازه کنید", 409)
			return
		}
		var d BusinessData
		if !newRow {
			json.Unmarshal(before, &d)
		}
		old := d
		d.Enabled = r.FormValue("enabled") == "on"
		if kind == "chat" {
			section = "integrations"
			d.DurationMinutes, e = strconv.Atoi(normalize(r.FormValue("duration_minutes")))
			if e != nil || d.DurationMinutes < 5 || d.DurationMinutes > 240 {
				a.fail(w, r, "مدت گفتگو باید بین ۵ تا ۲۴۰ دقیقه باشد", 400)
				return
			}
			d.Enabled = true
		} else if kind == "triage" {
			section = "triage"
			if r.FormValue("mode") == "simple" {
				for i := range d.Questions {
					q := &d.Questions[i]
					q.Label = strings.TrimSpace(r.FormValue("label-" + q.ID))
					q.Required = r.FormValue("required-"+q.ID) == "on"
					for j := range q.Options {
						q.Options[j].Label = strings.TrimSpace(r.FormValue("option-" + q.ID + "-" + q.Options[j].Value))
					}
				}
				e = validateQuestions(d.Questions)
			} else if e = json.Unmarshal([]byte(r.FormValue("questions")), &d.Questions); e == nil {
				e = validateQuestions(d.Questions)
			}
			if e != nil {
				a.fail(w, r, "پرسشنامه معتبر نیست: "+e.Error(), 400)
				return
			}
			d.Enabled = true
		} else {
			d.Name = strings.TrimSpace(r.FormValue("name"))
			if d.Name == "" || len(d.Name) > 300 {
				a.fail(w, r, "عنوان لازم است (حداکثر ۳۰۰ بایت)", 400)
				return
			}
			switch kind {
			case "service":
				d.Commission, e = strconv.Atoi(normalize(r.FormValue("commission")))
				if e != nil || d.Commission < 0 || d.Commission > 10000 {
					a.fail(w, r, "کمیسیون باید بین صفر و ۱۰۰۰۰ واحد باشد؛ ۱۰۰ واحد = یک درصد", 400)
					return
				}
			case "grooming", "sampling":
				d.Price, e = strconv.ParseInt(normalize(r.FormValue("price")), 10, 64)
				if e != nil || d.Price < 1 || d.Price > 1000000000 {
					a.fail(w, r, "قیمت مثبت و حداکثر یک میلیارد تومان لازم است", 400)
					return
				}
			case "banner", "article":
				section = "content"
				d.Body = strings.TrimSpace(r.FormValue("body"))
				d.Link = r.FormValue("link")
				if len(d.Body) > 50000 || (kind == "article" && d.Body == "") || len(d.Link) > 300 || kind == "banner" && !validInternalLink(d.Link) {
					a.fail(w, r, "متن یا لینک نامعتبر است؛ لینک بنر باید مسیر داخلی سایت باشد", 400)
					return
				}
				if kind == "article" {
					if d.Name != old.Name || d.Body != old.Body {
						d.ReviewedBy = ""
						d.ReviewedAt = nil
					}
					if d.Enabled && d.ReviewedBy == "" {
						a.fail(w, r, "مقاله پیش از انتشار باید توسط دامپزشک تأییدشده بازبینی شود", 409)
						return
					}
				}
			}
		}
		after, _ = json.Marshal(d)
		if newRow {
			_, e = tx.Exec(ctx, "INSERT INTO business_config(kind,id,data) VALUES($1,$2,$3)", kind, id, after)
		} else {
			_, e = tx.Exec(ctx, "UPDATE business_config SET data=$3,revision=revision+1 WHERE kind=$1 AND id=$2", kind, id, after)
		}
	}
	if e == nil {
		_, e = tx.Exec(ctx, "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,$2,$3,$4,$5)", sess(r).User.ID, kind, id, before, after)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "ذخیره نشد؛ دوباره تلاش کنید", 500)
		return
	}
	redirect(w, r, noticeURL("/management/"+section, "تغییرات ذخیره شد و در محصول اعمال می‌شود."))
}
func (a *App) businessPayout(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	ctx := r.Context()
	ref := strings.TrimSpace(r.FormValue("reference"))
	if len(ref) < 3 || len(ref) > 120 {
		a.fail(w, r, "شناسهٔ ثبت آزمایشی لازم است", 400)
		return
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	var provider, st, refund string
	var due int64
	e = tx.QueryRow(ctx, "SELECT COALESCE(provider_id,''),status,refund_status,amount-amount*commission_bps/10000 FROM bookings WHERE id=$1 AND paid_at IS NOT NULL FOR UPDATE", r.PathValue("id")).Scan(&provider, &st, &refund, &due)
	if e != nil || provider == "" || !contains([]string{"completed", "result_ready"}, st) || refund != "" {
		a.fail(w, r, "فقط رزرو تکمیل‌شدهٔ بدون پروندهٔ بازگشت قابل تسویه است", 409)
		return
	}
	tag, e := tx.Exec(ctx, "INSERT INTO business_payouts(booking_id,provider_id,amount,reference,actor_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING", r.PathValue("id"), provider, due, ref, sess(r).User.ID)
	if e == nil && tag.RowsAffected() == 0 {
		a.fail(w, r, "این رزرو قبلاً تسویه شده است", 409)
		return
	}
	if e == nil {
		raw, _ := json.Marshal(map[string]any{"provider": provider, "amount": due, "reference": ref, "mode": "simulation"})
		_, e = tx.Exec(ctx, "INSERT INTO business_audit(actor_id,kind,target,after_data) VALUES($1,'payout',$2,$3)", sess(r).User.ID, r.PathValue("id"), raw)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "تسویه ذخیره نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/management/finance", "ثبت تسویهٔ آزمایشی انجام شد؛ پولی منتقل نشده است."))
}
func csvSafe(s string) string {
	if strings.ContainsAny(strings.TrimLeft(s, " \t\r\n")[:min(1, len(strings.TrimLeft(s, " \t\r\n")))], "=+-@") {
		return "'" + s
	}
	return s
}
func (a *App) businessExport(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	rows, e := a.DB.Query(r.Context(), `SELECT b.id,b.service,b.status,b.amount,b.commission_bps,b.amount*b.commission_bps/10000,COALESCE(p.name,''),b.refund_amount,b.refund_status,COALESCE(x.reference,'') FROM bookings b LEFT JOIN providers p ON p.id=b.provider_id LEFT JOIN business_payouts x ON x.booking_id=b.id WHERE b.paid_at IS NOT NULL ORDER BY b.paid_at DESC`)
	if e != nil {
		a.fail(w, r, "خروجی آماده نشد", 500)
		return
	}
	defer rows.Close()
	var records [][]string
	records = append(records, []string{"booking", "service", "status", "gross_toman", "commission_bps", "fee_toman", "provider", "refund_toman", "refund_status", "simulation_payout_reference"})
	for rows.Next() {
		var id, svc, st, p, rs, ref string
		var amount, bps, fee, refund int64
		if e = rows.Scan(&id, &svc, &st, &amount, &bps, &fee, &p, &refund, &rs, &ref); e != nil {
			break
		}
		records = append(records, []string{id, svc, st, fmt.Sprint(amount), fmt.Sprint(bps), fmt.Sprint(fee), csvSafe(p), fmt.Sprint(refund), rs, csvSafe(ref)})
	}
	if e == nil {
		e = rows.Err()
	}
	if e != nil {
		a.fail(w, r, "خروجی آماده نشد", 500)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="keepet-finance.csv"`)
	w.Write([]byte("\xef\xbb\xbf"))
	cw := csv.NewWriter(w)
	cw.WriteAll(records)
}
func (a *App) article(w http.ResponseWriter, r *http.Request) {
	rs, e := a.records(r.Context(), "article")
	if e != nil {
		a.fail(w, r, "خواندن مقاله ناموفق بود", 500)
		return
	}
	for _, v := range rs {
		if v.ID == r.PathValue("slug") && v.Data.Enabled && v.Data.ReviewedBy != "" {
			p := a.page(r, v.Data.Name)
			p.Posts = []BusinessRecord{v}
			a.render(w, r, "article", p)
			return
		}
	}
	a.fail(w, r, "مقاله پیدا نشد", 404)
}
func (a *App) magazine(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "مجلهٔ کیپت")
	rs, e := a.records(r.Context(), "article")
	if e != nil {
		a.fail(w, r, "خواندن مجله ناموفق بود", 500)
		return
	}
	for _, v := range rs {
		if v.Data.Enabled && v.Data.ReviewedBy != "" {
			p.Posts = append(p.Posts, v)
		}
	}
	a.render(w, r, "magazine", p)
}
func (a *App) reviewArticle(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "provider") {
		return
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	var qualified bool
	e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM providers WHERE user_id=$1 AND service='vet' AND active AND verified)", sess(r).User.ID).Scan(&qualified)
	if e != nil || !qualified {
		a.fail(w, r, "فقط دامپزشک فعال و تأییدشده می‌تواند بازبینی کند", 403)
		return
	}
	var raw []byte
	var rev int64
	e = tx.QueryRow(ctx, "SELECT data,revision FROM business_config WHERE kind='article' AND id=$1 FOR UPDATE", r.PathValue("id")).Scan(&raw, &rev)
	if e != nil {
		a.fail(w, r, "مقاله پیدا نشد", 404)
		return
	}
	expected, _ := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	if rev != expected {
		a.fail(w, r, "متن تغییر کرده؛ دوباره مطالعه کنید", 409)
		return
	}
	var d BusinessData
	json.Unmarshal(raw, &d)
	now := time.Now()
	d.ReviewedBy = sess(r).User.Name
	d.ReviewedAt = &now
	after, _ := json.Marshal(d)
	_, e = tx.Exec(ctx, "UPDATE business_config SET data=$2,revision=revision+1 WHERE kind='article' AND id=$1", r.PathValue("id"), after)
	if e == nil {
		_, e = tx.Exec(ctx, "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,'scientific_review',$2,$3,$4)", sess(r).User.ID, r.PathValue("id"), raw, after)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "بازبینی ثبت نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/provider", "تأیید علمی ثبت شد؛ مدیر می‌تواند همین نسخه را منتشر کند."))
}
