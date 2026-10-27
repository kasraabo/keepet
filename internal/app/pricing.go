package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

type PriceQuote struct {
	Base, Markup, Fixed, Total, Hourly int64
	// Rounding is read only for archived quotes from the previous pricing version.
	Rounding     int64
	Minutes, BPS int
	Revision     int64
	Items        []MenuItem
}
type TariffItem struct {
	Minutes int

	ID, Name string
	Price    int64
	Enabled  bool
}
type TariffPage struct {
	ProviderID, Service string
	Revision, Hourly    int64
	Items               []TariffItem
	Example             PriceQuote
}

func (a *App) pricingRules(ctx context.Context, svc string) (BusinessData, error) {
	var raw []byte
	var d BusinessData
	e := a.DB.QueryRow(ctx, "SELECT data FROM business_config WHERE kind='service' AND id=$1", svc).Scan(&raw)
	if e == nil {
		e = json.Unmarshal(raw, &d)
	}
	if d.VisitMinutes == 0 {
		d.VisitMinutes = 30
	}
	return d, e
}
func (a *App) groomTariffs(ctx context.Context, id string) ([]TariffItem, error) {
	rows, e := a.DB.Query(ctx, `SELECT c.id,c.data->>'name',COALESCE(t.price,(c.data->>'price')::bigint),COALESCE(t.enabled,true),COALESCE(t.duration_minutes,NULLIF((c.data->>'duration_minutes')::integer,0),30) FROM business_config c LEFT JOIN provider_tariffs t ON t.service_id=c.id AND t.provider_id=$1 WHERE c.kind='grooming' AND (c.data->>'enabled')::boolean ORDER BY c.id`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []TariffItem
	for rows.Next() {
		var m TariffItem
		if e = rows.Scan(&m.ID, &m.Name, &m.Price, &m.Enabled, &m.Minutes); e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func addPriceFees(q *PriceQuote, d BusinessData) error {
	if q.Base <= 0 || q.Base > 10000000000 || d.Commission < 0 || d.Commission > 10000 || d.FixedFee < 0 || d.FixedFee > 1000000000 {
		return fmt.Errorf("تعرفه معتبر نیست")
	}
	q.BPS = d.Commission
	q.Markup = (q.Base*int64(d.Commission) + 9999) / 10000
	q.Fixed = d.FixedFee
	q.Total = q.Base + q.Markup + q.Fixed
	return nil
}
func (a *App) quote(ctx context.Context, b Booking) (PriceQuote, error) {
	q := PriceQuote{}
	if !contains([]string{"vet", "grooming", "sampling"}, b.Service) {
		return q, fmt.Errorf("نوع خدمت نامعتبر")
	}
	d, e := a.pricingRules(ctx, b.Service)
	if e != nil {
		return q, e
	}
	if !d.Enabled {
		return q, fmt.Errorf("خدمت غیرفعال است")
	}
	if b.Service == "sampling" {
		for _, m := range a.liveMenu(ctx, "sampling") {
			if m.ID == b.Sample {
				if m.Price <= 0 || d.Commission < 0 || d.Commission > 10000 {
					return q, fmt.Errorf("تعرفه معتبر نیست")
				}
				q.BPS = d.Commission
				q.Base = m.Price - m.Price*int64(d.Commission)/10000
				q.Markup = m.Price - q.Base
				q.Total = m.Price
				if b.TimeFlow {
					q.Minutes = d.DurationMinutes
					if q.Minutes == 0 {
						q.Minutes = 30
					}
				}
				return q, nil
			}
		}
		return q, fmt.Errorf("نوع نمونه معتبر نیست")
	}
	var svc string
	var legacy int64
	var hourly *int64
	e = a.DB.QueryRow(ctx, "SELECT service,price,hourly_rate,tariff_revision FROM providers WHERE id=$1 AND active AND verified", b.ProviderID).Scan(&svc, &legacy, &hourly, &q.Revision)
	if e != nil || svc != b.Service {
		return q, fmt.Errorf("متخصص در دسترس نیست")
	}
	if svc == "vet" {
		q.Hourly = legacy * 2
		if hourly != nil {
			q.Hourly = *hourly
		}
		q.Minutes = d.VisitMinutes
		q.Base = (q.Hourly*int64(q.Minutes) + 59) / 60
	} else {
		menu, e := a.groomTariffs(ctx, b.ProviderID)
		if e != nil {
			return q, e
		}
		seen := map[string]bool{}
		for _, id := range b.Items {
			if seen[id] {
				return q, fmt.Errorf("خدمت تکراری انتخاب شده")
			}
			seen[id] = true
			found := false
			for _, m := range menu {
				if m.ID == id && m.Enabled {
					q.Base += m.Price
					if b.TimeFlow {
						q.Minutes += m.Minutes
					}
					q.Items = append(q.Items, MenuItem{m.ID, m.Name, m.Price})
					found = true
					break
				}
			}
			if !found {
				return q, fmt.Errorf("خدمت انتخابی برای این گرومر فعال نیست")
			}
		}
		if q.Base == 0 {
			return q, fmt.Errorf("حداقل یک خدمت گرومینگ انتخاب کنید")
		}
	}
	e = addPriceFees(&q, d)
	return q, e
}
func (a *App) customerGroomMenu(ctx context.Context, id string) []MenuItem {
	ms, e := a.groomTariffs(ctx, id)
	if e != nil {
		return nil
	}
	d, e := a.pricingRules(ctx, "grooming")
	if e != nil {
		return nil
	}
	var out []MenuItem
	for _, m := range ms {
		if m.Enabled {
			q := PriceQuote{Base: m.Price}
			if addPriceFees(&q, d) == nil {
				out = append(out, MenuItem{m.ID, m.Name, q.Base})
			}
		}
	}
	return out
}
func (a *App) providerPricing(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "provider") {
		return
	}
	p := a.page(r, "تعرفه‌ها و درآمد من")
	t := &TariffPage{}
	var rate *int64
	var legacy int64
	e := a.DB.QueryRow(r.Context(), "SELECT id,service,tariff_revision,hourly_rate,price FROM providers WHERE user_id=$1 AND active", p.User.ID).Scan(&t.ProviderID, &t.Service, &t.Revision, &rate, &legacy)
	if e != nil || t.Service == "sampling" {
		a.fail(w, r, "تعرفهٔ این خدمت از مدیریت تعیین می‌شود", 403)
		return
	}
	t.Hourly = legacy * 2
	if rate != nil {
		t.Hourly = *rate
	}
	if t.Service == "grooming" {
		t.Items, e = a.groomTariffs(r.Context(), t.ProviderID)
	}
	if e != nil {
		a.fail(w, r, "تعرفه‌ها بارگذاری نشد", 500)
		return
	}
	if t.Service == "vet" {
		t.Example, _ = a.quote(r.Context(), Booking{Service: "vet", ProviderID: t.ProviderID})
	}
	p.Tariff = t
	a.render(w, r, "provider-pricing", p)
}
func (a *App) saveProviderPricing(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "provider") {
		return
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ثبت تعرفه انجام نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	var id, svc string
	var rev int64
	e = tx.QueryRow(ctx, "SELECT id,service,tariff_revision FROM providers WHERE user_id=$1 AND active FOR UPDATE", sess(r).User.ID).Scan(&id, &svc, &rev)
	if e != nil || !contains([]string{"vet", "grooming"}, svc) {
		a.fail(w, r, "دسترسی ندارید", 403)
		return
	}
	submitted, _ := strconv.ParseInt(normalize(r.FormValue("revision")), 10, 64)
	if submitted != rev {
		a.fail(w, r, "تعرفه تغییر کرده است؛ صفحه را تازه کنید", 409)
		return
	}
	parse := func(key string) (int64, error) {
		v, e := strconv.ParseInt(normalizeNumber(r.FormValue(key)), 10, 64)
		if e != nil || v < 1 || v > 1000000000 {
			return 0, fmt.Errorf("تعرفهٔ مثبت تا یک میلیارد تومان وارد کنید")
		}
		return v, nil
	}
	if svc == "vet" {
		v, err := parse("hourly_rate")
		if err != nil {
			a.fail(w, r, err.Error(), 400)
			return
		}
		_, e = tx.Exec(ctx, "UPDATE providers SET hourly_rate=$2,tariff_revision=tariff_revision+1 WHERE id=$1", id, v)
	} else {
		// Only centrally approved service IDs can be priced; ownership comes from the session.
		ms, err := a.groomTariffs(ctx, id)
		if err != nil {
			a.fail(w, r, "فهرست خدمات بارگذاری نشد", 500)
			return
		}
		for _, m := range ms {
			v, err := parse("price_" + m.ID)
			if err != nil {
				a.fail(w, r, err.Error(), 400)
				return
			}
			minutes := m.Minutes
			if raw := r.FormValue("minutes_" + m.ID); raw != "" {
				minutes, e = strconv.Atoi(normalize(raw))
				if e != nil || minutes < 5 || minutes > 480 {
					tx.Rollback(ctx)
					a.fail(w, r, "مدت هر خدمت باید بین ۵ تا ۴۸۰ دقیقه باشد", 400)
					return
				}
			}
			_, e = tx.Exec(ctx, `INSERT INTO provider_tariffs(provider_id,service_id,price,enabled,duration_minutes) VALUES($1,$2,$3,$4,$5) ON CONFLICT(provider_id,service_id) DO UPDATE SET price=EXCLUDED.price,enabled=EXCLUDED.enabled,duration_minutes=EXCLUDED.duration_minutes`, id, m.ID, v, r.FormValue("enabled_"+m.ID) == "on", minutes)
			if e != nil {
				break
			}
		}
		if e == nil {
			_, e = tx.Exec(ctx, "UPDATE providers SET tariff_revision=tariff_revision+1 WHERE id=$1", id)
		}
	}

	if e == nil {
		_, e = tx.Exec(ctx, "INSERT INTO business_audit(actor_id,kind,target,after_data) VALUES($1,'provider_tariff',$2,$3)", sess(r).User.ID, id, mustJSON(map[string]any{"revision": rev + 1}))
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "ذخیرهٔ تعرفه انجام نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/provider/pricing", "تعرفه ذخیره شد؛ رزروهای پرداخت‌شده تغییر نمی‌کنند."))
}
