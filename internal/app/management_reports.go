package app

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type KPICard struct{ Name, Value, Hint string }
type ReportTrend struct {
	Day    string
	Paid   int
	Amount int64
	Width  int
}
type ManagementReport struct {
	SLAEligible int

	From, To                                                                          string
	Start, End                                                                        time.Time
	Cards                                                                             []KPICard
	Trend                                                                             []ReportTrend
	Services                                                                          []BusinessMetric
	Paid, Drafts, Completed, Cancelled, Customers, Repeat, OnTime, Responded, Overdue int
	Gross, Refund, Fees, PreviousGross                                                int64
	AvailableCapacity, BookedCapacity                                                 int
}

func rate(n, d int) string {
	if d == 0 {
		return "—"
	}
	return digits(fmt.Sprintf("%.1f%%", float64(n)*100/float64(d)))
}
func reportRange(r *http.Request, loc *time.Location) (time.Time, time.Time, error) {
	now := time.Now().In(loc)
	today, _ := time.ParseInLocation("2006-01-02", now.Format("2006-01-02"), loc)
	from := today.AddDate(0, 0, -29)
	to := today
	var e error
	if s := r.URL.Query().Get("from"); s != "" {
		from, e = time.ParseInLocation("2006-01-02", s, loc)
		if e != nil {
			return from, to, e
		}
	}
	if s := r.URL.Query().Get("to"); s != "" {
		to, e = time.ParseInLocation("2006-01-02", s, loc)
		if e != nil {
			return from, to, e
		}
	}
	if from.After(to) || to.After(today) || to.Sub(from) > 365*24*time.Hour {
		return from, to, fmt.Errorf("invalid range")
	}
	return from, to.AddDate(0, 0, 1), nil
}
func (a *App) loadReport(ctx context.Context, r *http.Request) (*ManagementReport, error) {
	start, end, e := reportRange(r, a.Location)
	if e != nil {
		return nil, e
	}
	v := &ManagementReport{From: start.Format("2006-01-02"), To: end.AddDate(0, 0, -1).Format("2006-01-02"), Start: start, End: end}
	e = a.DB.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE status IN ('completed','result_ready')),count(*) FILTER(WHERE status IN ('cancel_requested','cancelled')),count(DISTINCT owner_id),COALESCE(sum(amount),0),COALESCE(sum(refund_amount) FILTER(WHERE refund_status='refunded'),0),COALESCE(sum(amount*commission_bps/10000) FILTER(WHERE status IN ('completed','result_ready')),0) FROM bookings WHERE paid_at>=$1 AND paid_at<$2`, start, end).Scan(&v.Paid, &v.Completed, &v.Cancelled, &v.Customers, &v.Gross, &v.Refund, &v.Fees)
	if e != nil {
		return nil, e
	}
	e = a.DB.QueryRow(ctx, "SELECT count(*) FROM bookings WHERE created_at>=$1 AND created_at<$2 AND status='draft'", start, end).Scan(&v.Drafts)
	if e != nil {
		return nil, e
	}
	e = a.DB.QueryRow(ctx, `SELECT count(*) FROM (SELECT owner_id FROM bookings WHERE paid_at>=$1 AND paid_at<$2 AND owner_id IS NOT NULL GROUP BY owner_id HAVING count(*)>=2) x`, start, end).Scan(&v.Repeat)
	if e != nil {
		return nil, e
	}
	e = a.DB.QueryRow(ctx, `SELECT COALESCE(sum(amount),0) FROM bookings WHERE paid_at>=$1 AND paid_at<$2`, start.Add(-end.Sub(start)), start).Scan(&v.PreviousGross)
	if e != nil {
		return nil, e
	}
	e = a.DB.QueryRow(ctx, `SELECT count(*) FILTER(WHERE first_response IS NOT NULL),count(*) FILTER(WHERE first_response<=paid_at+interval '30 minutes'),count(*) FILTER(WHERE first_response IS NOT NULL OR paid_at<=now()-interval '30 minutes') FROM (SELECT b.paid_at,(SELECT min(e.created_at) FROM booking_events e WHERE e.booking_id=b.id AND (e.event IN ('confirm','assign','replacement_pending') OR (e.event='cancel_requested' AND EXISTS(SELECT 1 FROM users u WHERE u.id=e.actor_id AND u.role IN ('admin','ops'))))) first_response FROM bookings b WHERE paid_at>=$1 AND paid_at<$2) x`, start, end).Scan(&v.Responded, &v.OnTime, &v.SLAEligible)
	if e != nil {
		return nil, e
	}
	e = a.DB.QueryRow(ctx, `SELECT count(*) FROM bookings b WHERE paid_at<now()-interval '30 minutes' AND status IN ('paid','confirmed','replacement_pending') AND NOT EXISTS(SELECT 1 FROM booking_events e WHERE e.booking_id=b.id AND (e.event IN ('confirm','assign','replacement_pending') OR (e.event='cancel_requested' AND EXISTS(SELECT 1 FROM users u WHERE u.id=e.actor_id AND u.role IN ('admin','ops')))))`).Scan(&v.Overdue)
	if e != nil {
		return nil, e
	}
	e = a.DB.QueryRow(ctx, `SELECT COALESCE(sum(a.capacity),0),COALESCE(sum((SELECT count(*) FROM bookings b WHERE b.availability_id=a.id AND b.payment_status='paid' AND b.status NOT IN ('cancelled','cancel_requested'))),0) FROM availability a JOIN providers p ON p.id=a.provider_id WHERE p.active AND p.verified AND a.day>=(now() AT TIME ZONE 'Asia/Tehran')::date AND a.day<(now() AT TIME ZONE 'Asia/Tehran')::date+14 AND ((a.day+a.end_time) AT TIME ZONE 'Asia/Tehran')>now() AND p.species && ARRAY['cat','dog'] AND EXISTS(SELECT 1 FROM business_config c WHERE c.kind='service' AND c.id=p.service AND (c.data->>'enabled')::boolean) AND (p.service='vet' OR EXISTS(SELECT 1 FROM business_config c WHERE c.kind='area' AND c.id=ANY(p.areas) AND (c.data->>'enabled')::boolean))`).Scan(&v.AvailableCapacity, &v.BookedCapacity)
	if e != nil {
		return nil, e
	}
	// Creation cohort is used for draft-to-payment conversion; payment period is used for financial KPIs.
	var started, converted int
	e = a.DB.QueryRow(ctx, "SELECT count(*),count(*) FILTER(WHERE paid_at IS NOT NULL) FROM bookings WHERE created_at>=$1 AND created_at<$2", start, end).Scan(&started, &converted)
	if e != nil {
		return nil, e
	}
	avg := "—"
	if v.Paid > 0 {
		avg = money(v.Gross / int64(v.Paid))
	}
	growth := "—"
	if v.PreviousGross > 0 {
		growth = digits(fmt.Sprintf("%+.1f%%", float64(v.Gross-v.PreviousGross)*100/float64(v.PreviousGross)))
	}
	v.Cards = []KPICard{
		{"ارزش پرداخت‌ها (GMV)", money(v.Gross), "تومان · کل پرداخت‌های ثبت‌شده در بازه"},
		{"دریافتی پس از بازگشت وجه", money(v.Gross - v.Refund), "تومان · درآمد خالص کسب‌وکار نیست"},
		{"کمیسیون خدمات تکمیل‌شده", money(v.Fees), "تومان · بر اساس نرخ زمان پرداخت"},
		{"میانگین مبلغ سفارش (AOV)", avg, "تومان · ارزش پرداخت تقسیم بر تعداد پرداخت"},
		{"سفارش‌های پرداخت‌شده", digits(strconv.Itoa(v.Paid)), "بر اساس زمان ثبت پرداخت"},
		{"رشد ارزش پرداخت‌ها", growth, "نسبت به دورهٔ پیشین با همین طول"},
		{"نرخ تکمیل خدمت", rate(v.Completed, v.Paid), "وضعیت فعلی رزروهای پرداخت‌شدهٔ این دوره"},
		{"نرخ درخواست کنسلی", rate(v.Cancelled, v.Paid), "لغوشده یا در انتظار کنسلی / پرداخت‌شده"},
		{"تبدیل درخواست به پرداخت", rate(converted, started), "درخواست‌های ایجادشده در این دوره و وضعیت فعلی آن‌ها"},
		{"مشتریان پرداخت‌کننده", digits(strconv.Itoa(v.Customers)), "حساب‌های یکتای دارای پرداخت در این دوره"},
		{"مشتریان با خرید تکراری", rate(v.Repeat, v.Customers), "دو یا چند پرداخت در همین دوره"},
		{"ثبت اقدام عملیات تا ۳۰ دقیقه", rate(v.OnTime, v.SLAEligible), "فقط درخواست‌های پاسخ‌داده‌شده یا با عمر ≥۳۰ دقیقه؛ تماس واقعی نیست"},
	}
	rows, e := a.DB.Query(ctx, `SELECT s.day::date::text,count(b.id),COALESCE(sum(b.amount),0) FROM generate_series($1::date,$2::date-1,interval '1 day') s(day) LEFT JOIN bookings b ON (b.paid_at AT TIME ZONE 'Asia/Tehran')::date=s.day::date GROUP BY s.day ORDER BY s.day`, v.From, end.Format("2006-01-02"))
	if e != nil {
		return nil, e
	}
	var maximum int64
	for rows.Next() {
		var t ReportTrend
		if e = rows.Scan(&t.Day, &t.Paid, &t.Amount); e != nil {
			break
		}
		if t.Amount > maximum {
			maximum = t.Amount
		}
		v.Trend = append(v.Trend, t)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return nil, e
	}
	for i := range v.Trend {
		if maximum > 0 {
			v.Trend[i].Width = int(v.Trend[i].Amount * 100 / maximum)
		}
	}
	rows, e = a.DB.Query(ctx, `SELECT service,count(*),COALESCE(sum(amount),0),COALESCE(sum(amount*commission_bps/10000) FILTER(WHERE status IN ('completed','result_ready')),0) FROM bookings WHERE paid_at>=$1 AND paid_at<$2 GROUP BY service ORDER BY service`, start, end)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var s BusinessMetric
		if e = rows.Scan(&s.Name, &s.Count, &s.Amount, &s.Commission); e != nil {
			break
		}
		v.Services = append(v.Services, s)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	return v, e
}
func (a *App) reportExport(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	v, e := a.loadReport(r.Context(), r)
	if e != nil {
		a.fail(w, r, "بازهٔ گزارش نامعتبر است یا گزارش در دسترس نیست", 400)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="keepet-kpi-report.csv"`)
	w.Write([]byte("\xef\xbb\xbf"))
	cw := csv.NewWriter(w)
	records := [][]string{{"from", "to", "metric", "value", "definition"}}
	for _, c := range v.Cards {
		records = append(records, []string{v.From, v.To, csvSafe(c.Name), csvSafe(c.Value), csvSafe(c.Hint)})
	}
	cw.WriteAll(records)
}
