package app

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type LabItem struct {
	ID, Name, Sample string
	Cost             int64
	Revision         int64
}
type LabQuote struct {
	LabAddress string `json:",omitempty"`

	PackageID, PackageName, LabID, LabName string
	Revision, LabRevision, Cost            int64
	Items                                  []LabItem
}
type Laboratory struct {
	ID, Name, Address string
	Active            bool
	Revision          int64
}
type LabTest struct {
	ID, LabID, Name, Sample string
	Cost                    int64
	Active                  bool
	Revision                int64
}
type LabPackage struct {
	ID, Name string
	Active   bool
	Revision int64
	Tests    []string
}
type LabReport struct {
	ID, Body, Filename, State string
	At                        time.Time
	PublishedAt               *time.Time
}
type LabBill struct {
	ID, Name, Status, Reference string
	Cost                        int64
	Paid                        bool
}
type LabPage struct {
	CurrentID string

	Labs        []Laboratory
	Tests       []LabTest
	Packages    []LabPackage
	Bills       []LabBill
	Quote       *LabQuote
	Reports     []LabReport
	Ops         bool
	Outstanding int64
}

// Example records are visibly marked as development data. Existing edits survive restarts.
func (a *App) seedLabs(ctx context.Context) error {
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT id FROM lab_catalog WHERE id=1 FOR UPDATE"); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO laboratories(id,name,address) VALUES('demo-lab','آزمایشگاه نمونه (آزمایشی)','اطلاعات نمونه؛ پیش از استفاده واقعی اصلاح شود') ON CONFLICT DO NOTHING`)
	if e != nil {
		return e
	}
	for _, m := range Samples {
		if _, e = tx.Exec(ctx, `INSERT INTO lab_tests(id,lab_id,name,sample,cost) VALUES($1,'demo-lab',$2,$1,100000) ON CONFLICT DO NOTHING`, m.ID, m.Name); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO lab_packages(id,name) VALUES($1,$2) ON CONFLICT DO NOTHING`, m.ID, m.Name); e != nil {
			return e
		}
		// Only seed the original package once, never restore deliberately removed components.
		if _, e = tx.Exec(ctx, `INSERT INTO lab_package_items(package_id,test_id) SELECT $1,$1 WHERE EXISTS(SELECT 1 FROM lab_packages WHERE id=$1 AND revision=1) ON CONFLICT DO NOTHING`, m.ID); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func (a *App) labQuote(ctx context.Context, id string) (*LabQuote, error) {
	q := &LabQuote{PackageID: id}
	e := a.DB.QueryRow(ctx, "SELECT name,revision FROM lab_packages WHERE id=$1 AND active", id).Scan(&q.PackageName, &q.Revision)
	if e != nil {
		return nil, fmt.Errorf("پکیج آزمایش فعال نیست")
	}
	rows, e := a.DB.Query(ctx, `SELECT t.id,t.name,t.sample,t.cost,t.revision,l.id,l.name,l.address,l.revision,t.active AND l.active FROM lab_package_items i JOIN lab_tests t ON t.id=i.test_id JOIN laboratories l ON l.id=t.lab_id WHERE i.package_id=$1 ORDER BY t.id`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var m LabItem
		var lid, ln, address string
		var lr int64
		var active bool
		if e = rows.Scan(&m.ID, &m.Name, &m.Sample, &m.Cost, &m.Revision, &lid, &ln, &address, &lr, &active); e != nil {
			return nil, e
		}
		if !active || (q.LabID != "" && q.LabID != lid) {
			return nil, fmt.Errorf("اجزای پکیج یا آزمایشگاه در دسترس نیستند")
		}
		q.LabID, q.LabName, q.LabRevision, q.LabAddress = lid, ln, lr, address
		q.Cost += m.Cost
		q.Items = append(q.Items, m)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if len(q.Items) == 0 || q.Cost > 10000000000 {
		return nil, fmt.Errorf("پکیج آزمایش معتبر نیست")
	}
	return q, nil
}
func (a *App) labPackageAvailable(ctx context.Context, id string) bool {
	_, e := a.labQuote(ctx, id)
	return e == nil
}
func (a *App) labMenu(ctx context.Context) []MenuItem {
	rows, e := a.DB.Query(ctx, "SELECT id FROM lab_packages WHERE active ORDER BY name,id")
	if e != nil {
		return nil
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	var out []MenuItem
	for _, id := range ids {
		if q, e := a.labQuote(ctx, id); e == nil {
			out = append(out, MenuItem{id, q.PackageName, q.Cost})
		}
	}
	return out
}
func (a *App) samplingQuote(ctx context.Context, b Booking, d BusinessData) (PriceQuote, error) {
	q := PriceQuote{}
	l, e := a.labQuote(ctx, b.Sample)
	if e != nil {
		return q, e
	}
	q.Lab = l
	if b.ProviderID != "" {
		e = a.DB.QueryRow(ctx, "SELECT sampling_rate,tariff_revision FROM providers WHERE id=$1 AND active AND verified AND service='sampling'", b.ProviderID).Scan(&q.Base, &q.Revision)
	} else {
		e = a.DB.QueryRow(ctx, "SELECT COALESCE(min(sampling_rate),0) FROM providers WHERE service='sampling' AND active AND verified AND $1=ANY(species) AND $2=ANY(areas)", b.Species, b.Area).Scan(&q.Base)
	}
	if e != nil {
		return q, fmt.Errorf("تعرفه نمونه‌بردار در دسترس نیست")
	}
	if e = addPriceFees(&q, d); e != nil {
		return q, e
	}
	q.Total += l.Cost
	q.Minutes = d.DurationMinutes
	if q.Minutes == 0 {
		q.Minutes = 30
	}
	return q, nil
}
func (a *App) labManagement(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin", "ops") {
		return
	}
	p := a.page(r, "آزمایشگاه، آزمایش‌ها و پکیج‌ها")
	v := &LabPage{}
	p.Laboratory = v
	ctx := r.Context()
	rows, e := a.DB.Query(ctx, "SELECT id,name,address,active,revision FROM laboratories ORDER BY id")
	if e != nil {
		a.fail(w, r, "اطلاعات بارگذاری نشد", 500)
		return
	}
	for rows.Next() {
		var l Laboratory
		if e = rows.Scan(&l.ID, &l.Name, &l.Address, &l.Active, &l.Revision); e != nil {
			break
		}
		v.Labs = append(v.Labs, l)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		a.fail(w, r, "اطلاعات بارگذاری نشد", 500)
		return
	}
	rows, e = a.DB.Query(ctx, "SELECT id,lab_id,name,sample,cost,active,revision FROM lab_tests ORDER BY id")
	if e != nil {
		a.fail(w, r, "اطلاعات بارگذاری نشد", 500)
		return
	}
	for rows.Next() {
		var l LabTest
		if e = rows.Scan(&l.ID, &l.LabID, &l.Name, &l.Sample, &l.Cost, &l.Active, &l.Revision); e != nil {
			break
		}
		v.Tests = append(v.Tests, l)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		a.fail(w, r, "اطلاعات بارگذاری نشد", 500)
		return
	}
	rows, e = a.DB.Query(ctx, "SELECT p.id,p.name,p.active,p.revision,COALESCE(array_agg(i.test_id ORDER BY i.test_id) FILTER(WHERE i.test_id IS NOT NULL),'{}') FROM lab_packages p LEFT JOIN lab_package_items i ON i.package_id=p.id GROUP BY p.id ORDER BY p.id")
	if e != nil {
		a.fail(w, r, "اطلاعات بارگذاری نشد", 500)
		return
	}
	for rows.Next() {
		var l LabPackage
		if e = rows.Scan(&l.ID, &l.Name, &l.Active, &l.Revision, &l.Tests); e != nil {
			break
		}
		v.Packages = append(v.Packages, l)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		a.fail(w, r, "اطلاعات بارگذاری نشد", 500)
		return
	}
	rows, e = a.DB.Query(ctx, `SELECT b.id,COALESCE(b.pricing_snapshot->'Lab'->>'LabName',''),b.status,b.lab_cost,x.booking_id IS NOT NULL,COALESCE(x.reference,'') FROM bookings b LEFT JOIN lab_payments x ON x.booking_id=b.id WHERE b.lab_flow AND b.payment_status='paid' AND b.refund_status='' AND b.settlement_approved_at IS NOT NULL ORDER BY b.paid_at DESC LIMIT 500`)
	if e != nil {
		a.fail(w, r, "اطلاعات بارگذاری نشد", 500)
		return
	}
	for rows.Next() {
		var b LabBill
		if e = rows.Scan(&b.ID, &b.Name, &b.Status, &b.Cost, &b.Paid, &b.Reference); e != nil {
			break
		}
		v.Bills = append(v.Bills, b)
		if !b.Paid {
			v.Outstanding += b.Cost
		}
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		a.fail(w, r, "اطلاعات بارگذاری نشد", 500)
		return
	}
	a.render(w, r, "laboratories", p)
}
func (a *App) saveLabCatalog(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	ctx := r.Context()
	kind, id := r.FormValue("kind"), r.FormValue("id")
	table := map[string]string{"lab": "laboratories", "test": "lab_tests", "package": "lab_packages"}[kind]
	if table == "" || !businessID.MatchString(id) {
		a.fail(w, r, "نوع و شناسه معتبر انتخاب کنید", 400)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if len(name) < 3 || len(name) > 300 {
		a.fail(w, r, "عنوان معتبر وارد کنید", 400)
		return
	}
	rev, e := strconv.ParseInt(normalize(r.FormValue("revision")), 10, 64)
	if e != nil || rev < 0 {
		a.fail(w, r, "نسخه معتبر نیست", 400)
		return
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	fail := func(msg string, c int) { tx.Rollback(ctx); a.fail(w, r, msg, c) }
	if _, e = tx.Exec(ctx, "SELECT id FROM lab_catalog WHERE id=1 FOR UPDATE"); e != nil {
		fail("ذخیره نشد", 500)
		return
	}
	var old []byte
	var current int64
	e = tx.QueryRow(ctx, "SELECT revision,to_jsonb(t) FROM "+table+" t WHERE id=$1 FOR UPDATE", id).Scan(&current, &old)
	if (e == pgx.ErrNoRows && rev != 0) || (e == nil && (rev == 0 || rev != current)) {
		fail("اطلاعات تغییر کرده؛ صفحه را تازه کنید", 409)
		return
	}
	if e != nil && e != pgx.ErrNoRows {
		fail("ذخیره نشد", 500)
		return
	}
	active := r.FormValue("active") == "on"
	switch kind {
	case "lab":
		address := strings.TrimSpace(r.FormValue("address"))
		if len(address) > 2000 {
			fail("آدرس کوتاه‌تر وارد کنید", 400)
			return
		}
		_, e = tx.Exec(ctx, "INSERT INTO laboratories(id,name,address,active) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET name=$2,address=$3,active=$4,revision=laboratories.revision+1", id, name, address, active)
	case "test":
		sample := r.FormValue("sample")
		cost, err := strconv.ParseInt(normalizeNumber(r.FormValue("cost")), 10, 64)
		if err != nil || cost < 0 || cost > 1000000000 || !contains([]string{"blood", "urine", "stool"}, sample) {
			fail("هزینه و نوع نمونه معتبر وارد کنید؛ فقط نمونه‌های ساده مجازند", 400)
			return
		}
		_, e = tx.Exec(ctx, "INSERT INTO lab_tests(id,lab_id,name,sample,cost,active) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO UPDATE SET lab_id=$2,name=$3,sample=$4,cost=$5,active=$6,revision=lab_tests.revision+1", id, r.FormValue("lab_id"), name, sample, cost, active)
	case "package":
		ids := r.Form["tests"]
		if len(ids) < 1 || len(ids) > 50 {
			fail("یک تا پنجاه آزمایش انتخاب کنید", 400)
			return
		}
		seen := map[string]bool{}
		var lab string
		for _, tid := range ids {
			if seen[tid] {
				fail("آزمایش تکراری انتخاب شده", 400)
				return
			}
			seen[tid] = true
			var lid string
			if err := tx.QueryRow(ctx, "SELECT lab_id FROM lab_tests WHERE id=$1", tid).Scan(&lid); err != nil || (lab != "" && lab != lid) {
				fail("اجزای هر پکیج باید متعلق به یک آزمایشگاه باشند", 400)
				return
			}
			lab = lid
		}
		_, e = tx.Exec(ctx, "INSERT INTO lab_packages(id,name,active) VALUES($1,$2,$3) ON CONFLICT(id) DO UPDATE SET name=$2,active=$3,revision=lab_packages.revision+1", id, name, active)
		if e == nil {
			_, e = tx.Exec(ctx, "DELETE FROM lab_package_items WHERE package_id=$1", id)
		}
		for _, tid := range ids {
			if e == nil {
				_, e = tx.Exec(ctx, "INSERT INTO lab_package_items(package_id,test_id) VALUES($1,$2)", id, tid)
			}
		}
	}
	if e == nil {
		_, e = tx.Exec(ctx, "UPDATE lab_catalog SET revision=revision+1 WHERE id=1")
	}
	if e == nil {
		_, e = tx.Exec(ctx, "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) SELECT $1,$2,$3,$4,to_jsonb(t) FROM "+table+" t WHERE id=$3", sess(r).User.ID, "lab_"+kind, id, old)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		fail("ذخیره نشد؛ آزمایشگاه و اطلاعات فرم را بررسی کنید", 409)
		return
	}
	redirect(w, r, noticeURL("/management/laboratories", "کاتالوگ ذخیره شد؛ درخواست‌های ثبت‌شده تغییر نمی‌کنند."))
}
func (a *App) labOrderPage(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	b, e := a.getBooking(r)
	if e != nil || b.Service != "sampling" {
		a.fail(w, r, "سفارش پیدا نشد", 404)
		return
	}
	u := sess(r).User
	var provider string
	a.DB.QueryRow(r.Context(), "SELECT COALESCE(user_id,'') FROM providers WHERE id=$1", b.ProviderID).Scan(&provider)
	ops := contains([]string{"admin", "ops"}, u.Role)
	if u.ID != b.OwnerID && u.ID != provider && !ops {
		a.fail(w, r, "دسترسی ندارید", 403)
		return
	}
	p := a.page(r, "نتیجهٔ آزمایش")
	p.Booking = &b
	v := &LabPage{Ops: ops}
	p.Laboratory = v
	var raw []byte
	a.DB.QueryRow(r.Context(), "SELECT pricing_snapshot,COALESCE(lab_report_id,'') FROM bookings WHERE id=$1", b.ID).Scan(&raw, &v.CurrentID)
	var q PriceQuote
	json.Unmarshal(raw, &q)
	v.Quote = q.Lab
	rows, e := a.DB.Query(r.Context(), "SELECT id,body,filename,state,created_at,published_at FROM lab_reports WHERE booking_id=$1 AND ($2 OR state='published') ORDER BY (id=$3) DESC,created_at DESC,id DESC", b.ID, ops, v.CurrentID)
	if e != nil {
		a.fail(w, r, "نتیجه بارگذاری نشد", 500)
		return
	}
	for rows.Next() {
		var v2 LabReport
		if e = rows.Scan(&v2.ID, &v2.Body, &v2.Filename, &v2.State, &v2.At, &v2.PublishedAt); e != nil {
			break
		}
		v.Reports = append(v.Reports, v2)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		a.fail(w, r, "نتیجه بارگذاری نشد", 500)
		return
	}
	if v.CurrentID == "" {
		for _, r := range v.Reports {
			if r.State == "published" {
				v.CurrentID = r.ID
				break
			}
		}
	}
	a.render(w, r, "lab-order", p)
}
func (a *App) labOrderAction(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin", "ops") {
		return
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	fail := func(msg string, c int) { tx.Rollback(ctx); a.fail(w, r, msg, c) }
	b, e := scanBooking(tx.QueryRow(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE id=$1 FOR UPDATE", r.PathValue("id")))
	if e != nil || b.Service != "sampling" {
		fail("سفارش پیدا نشد", 404)
		return
	}
	if b.PaymentStatus != "paid" || b.RefundStatus != "" || !contains([]string{"delivered", "result_ready"}, b.Status) || (b.FulfillmentFlow && b.SettlementApprovedAt == nil) {
		fail("نتیجه پس از تأیید تحویل نمونه قابل ثبت است", 409)
		return
	}
	detail := "پیش‌نویس نتیجهٔ آزمایش ذخیره شد؛ هنوز برای مشتری منتشر نشده است."
	event := "lab_report_draft"
	switch r.FormValue("action") {
	case "draft":
		if b.Result != "" {
			_, e = tx.Exec(ctx, "INSERT INTO lab_reports(id,booking_id,body,uploaded_by,state,published_by,published_at) SELECT $1,$2,$3,$4,'published',$4,now() WHERE NOT EXISTS(SELECT 1 FROM lab_reports WHERE booking_id=$2)", token(), b.ID, b.Result, sess(r).User.ID)
			if e != nil {
				fail("حفظ سابقهٔ نتیجه انجام نشد", 500)
				return
			}
		}
		body := strings.TrimSpace(r.FormValue("body"))
		if len(body) > 20000 {
			fail("متن نتیجه کوتاه‌تر وارد کنید", 400)
			return
		}
		var data []byte
		var mt, fn string
		if r.MultipartForm != nil && len(r.MultipartForm.File["receipt"]) > 0 {
			data, mt, fn, e = readHandover(r)
			if e != nil {
				fail(e.Error(), 400)
				return
			}
			fn = strings.Replace(fn, "handover", "result", 1)
		}
		if body == "" && len(data) == 0 {
			fail("متن یا فایل نتیجه لازم است", 400)
			return
		}
		_, e = tx.Exec(ctx, "INSERT INTO lab_reports(id,booking_id,body,filename,mime,data,uploaded_by) VALUES($1,$2,$3,$4,$5,$6,$7)", token(), b.ID, body, fn, mt, data, sess(r).User.ID)
	case "publish":
		id := r.FormValue("report_id")
		var body, state string
		var created time.Time
		e = tx.QueryRow(ctx, "SELECT body,state,created_at FROM lab_reports WHERE id=$1 AND booking_id=$2 FOR UPDATE", id, b.ID).Scan(&body, &state, &created)
		if e != nil {
			fail("نسخهٔ نتیجه معتبر نیست", 409)
			return
		}
		if state == "published" {
			redirect(w, r, "/bookings/"+b.ID+"/laboratory")
			return
		}
		var stale bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM lab_reports WHERE booking_id=$1 AND state='published' AND created_at>$2)", b.ID, created).Scan(&stale); e != nil || stale {
			fail("نتیجهٔ جدیدتری منتشر شده؛ نسخهٔ درست را دوباره بررسی کنید", 409)
			return
		}
		_, e = tx.Exec(ctx, "UPDATE lab_reports SET state='published',published_by=$2,published_at=now() WHERE id=$1", id, sess(r).User.ID)
		if e == nil {
			_, e = tx.Exec(ctx, "UPDATE bookings SET lab_report_id=$2,result=$3,status='result_ready' WHERE id=$1", b.ID, id, body)
		}
		event = "result_ready"
		detail = "نتیجهٔ آزمایش توسط اپراتور منتشر شد؛ متن و فایل در پروندهٔ سفارش قابل مشاهده است."
	default:
		fail("اقدام معتبر انتخاب کنید", 400)
		return
	}
	// Draft content and its event are private to operations, not sent to the customer.
	if e == nil {
		if event == "lab_report_draft" {
			_, e = tx.Exec(ctx, "INSERT INTO business_audit(actor_id,kind,target,after_data) VALUES($1,'lab_report_draft',$2,'{}')", sess(r).User.ID, b.ID)
		} else {
			e = fulfillmentEvent(ctx, tx, b, sess(r).User.ID, event, detail)
		}
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		fail("ثبت نتیجه انجام نشد", 409)
		return
	}
	redirect(w, r, noticeURL("/bookings/"+b.ID+"/laboratory", "نتیجه ثبت شد."))
}
func (a *App) labReportDownload(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	var data []byte
	var mt, fn string
	u := sess(r).User
	ops := contains([]string{"admin", "ops"}, u.Role)
	e := a.DB.QueryRow(r.Context(), `SELECT l.data,l.mime,l.filename FROM lab_reports l JOIN bookings b ON b.id=l.booking_id LEFT JOIN providers p ON p.id=b.provider_id WHERE l.id=$1 AND b.id=$2 AND l.data IS NOT NULL AND ($3 OR (l.state='published' AND (b.owner_id=$4 OR p.user_id=$4)))`, r.PathValue("report"), r.PathValue("id"), ops, u.ID).Scan(&data, &mt, &fn)
	if e != nil {
		a.fail(w, r, "فایل نتیجه در دسترس نیست", 404)
		return
	}
	servePrivateLabFile(w, data, mt, fn)
}
func (a *App) recordLabPayment(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin", "ops") {
		return
	}
	ctx := r.Context()
	reference := strings.TrimSpace(r.FormValue("reference"))
	if len(reference) < 3 || len(reference) > 300 {
		a.fail(w, r, "مشخصات پرداخت انجام‌شده را وارد کنید", 400)
		return
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	b, e := scanBooking(tx.QueryRow(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE id=$1 FOR UPDATE", r.PathValue("id")))
	if e != nil || !b.LabFlow || b.LabCost <= 0 || b.SettlementApprovedAt == nil || b.PaymentStatus != "paid" || b.RefundStatus != "" {
		a.fail(w, r, "هزینهٔ قابل تسویه پس از تحویل تأییدشده لازم است", 409)
		return
	}
	_, e = tx.Exec(ctx, "INSERT INTO lab_payments(booking_id,amount,reference,actor_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", b.ID, b.LabCost, reference, sess(r).User.ID)
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "ثبت پرداخت انجام نشد", 409)
		return
	}
	redirect(w, r, noticeURL("/management/laboratories", "سابقهٔ پرداخت آزمایشگاه ثبت شد؛ انتقال بانکی از سایت انجام نشده است."))
}

func servePrivateLabFile(w http.ResponseWriter, data []byte, mt, fn string) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", mt)
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": fn}))
	w.Write(data)
}
