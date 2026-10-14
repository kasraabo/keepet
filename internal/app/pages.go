package app

import (
	"net/http"
	"strings"
)

func (a *App) tickets(r *http.Request, user string) ([]Ticket, error) {
	rows, err := a.DB.Query(r.Context(), "SELECT id,subject,body,reply,status,email FROM tickets WHERE ($1='' OR user_id=$1) ORDER BY created_at DESC", user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ts []Ticket
	for rows.Next() {
		var t Ticket
		if err = rows.Scan(&t.ID, &t.Subject, &t.Body, &t.Reply, &t.Status, &t.Email); err != nil {
			return nil, err
		}
		ts = append(ts, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range ts {
		ts[i].Form = a.savedForm(r.Context(), "ticket", ts[i].ID)
		extra := []Question{}
		for _, q := range ts[i].Form.Questions {
			if !contains([]string{"subject", "email", "body"}, q.ID) {
				extra = append(extra, q)
			}
		}
		ts[i].Form.Questions = extra
	}
	return ts, nil
}
func (a *App) support(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "کنارتان هستیم")
	p.Form = a.form(r.Context(), "ticket", nil)
	p.FAQs, _ = a.records(r.Context(), "faq")
	if p.User != nil {
		p.Tickets, _ = a.tickets(r, p.User.ID)
	}
	a.render(w, r, "support", p)
}
func (a *App) ticket(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	f := a.form(r.Context(), "ticket", nil)
	answers, e := formAnswers(r, f)
	if e != nil {
		a.fail(w, r, e.Error(), 400)
		return
	}
	subject := strings.TrimSpace(r.FormValue("subject"))
	body := strings.TrimSpace(r.FormValue("body"))
	email := trimmed(r.FormValue("email"))
	if !validEmail(email) {
		a.fail(w, r, "ایمیل معتبر برای پیگیری تیکت لازم است", 400)
		return
	}
	if subject == "" || len(body) < 5 {
		a.fail(w, r, "موضوع و توضیح را کامل کنید", 400)
		return
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(r.Context())
	id := token()
	_, err := tx.Exec(r.Context(), "INSERT INTO tickets(id,user_id,subject,body,email) VALUES($1,$2,$3,$4,$5)", id, sess(r).User.ID, subject, body, email)
	if err == nil {
		err = saveFormResponse(r.Context(), tx, f, id, sess(r).User.ID, answers)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.fail(w, r, "درخواست پشتیبانی ثبت نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/support", "درخواست شما ثبت شد؛ پاسخ همین‌جا نمایش داده می‌شود."))
}
func (a *App) replyTicket(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "ops", "admin") {
		return
	}
	reply := strings.TrimSpace(r.FormValue("reply"))
	if reply == "" {
		a.fail(w, r, "پاسخ خالی است", 400)
		return
	}
	_, err := a.DB.Exec(r.Context(), "UPDATE tickets SET reply=$2,status='closed' WHERE id=$1", r.PathValue("id"), reply)
	if err != nil {
		a.fail(w, r, "پاسخ ثبت نشد", 500)
		return
	}
	redirect(w, r, opsHome(r))
}
func (a *App) publicPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if d, ok := a.customPage(r, slug); ok {
		p := a.page(r, d.Name)
		p.Content = d.Body
		a.render(w, r, "public", p)
		return
	}
	if slug == "magazine" {
		a.magazine(w, r)
		return
	}
	content := map[string][2]string{"about": {"مراقبت، با خیال راحت", "کیپت برای آسان‌تر کردن مراقبت از حیوانات خانگی ساخته می‌شود. سه خدمت اصلی با هماهنگی انسانی تیم عملیات ارائه می‌شوند؛ رزروِ آنی نداریم و زمان دقیق پس از هماهنگی مشخص می‌شود."}, "terms": {"قوانین و مقررات", "ثبت درخواست به معنی تأیید فوری نوبت نیست. پس از پیش‌پرداخت، تیم عملیات هماهنگی را انجام می‌دهد. اطلاعات حیوان و آدرس باید دقیق باشند. این نسخه آزمایشی است و پرداخت واقعی یا ارائهٔ خدمت واقعی انجام نمی‌دهد. متن حقوقی پیش از راه‌اندازی عمومی باید بازبینی شود."}, "privacy": {"حریم خصوصی", "شماره، اطلاعات پت، نشانی و سوابق مشاوره برای هماهنگی خدمت نگهداری می‌شوند. اطلاعات رزرو فقط برای صاحب آن، ارائه‌دهندهٔ تخصیص‌یافته و تیم مجاز عملیات قابل دسترسی است. داده‌های پزشکی و حساب در کش آفلاین ذخیره نمی‌شوند."}, "refund": {"کنسلی و بازگشت وجه", "پیش از تأیید یا حداقل ۶ ساعت پیش از زمان دقیق نوبت، بازگشت کامل است. کمتر از ۶ ساعت یا عدم حضور: ۱۰٪ وجه بازمی‌گردد و ۹۰٪ جریمه است. عدم تحقق از سمت کیپت: بازگشت کامل و بررسی جبران. شکایت کیفیت به صورت موردی توسط عملیات بررسی می‌شود. در نسخهٔ توسعه بازگشت وجه صرفاً آزمایشی است."}, "telemedicine": {"محدودیت‌های مشاورهٔ آنلاین", "مشاورهٔ آنلاین مکمل مراقبت است و جایگزین معاینهٔ حضوری نیست. پرسشنامه فقط داده جمع‌آوری می‌کند و تشخیص یا درجهٔ فوریت نمایش نمی‌دهد. در شرایط حاد به مرکز حضوری دامپزشکی مراجعه کنید. نسخه صرفاً سند مشاوره است؛ فروش، سفارش و ارسال آنلاین دارو و واکسن در کیپت وجود ندارد."}, "contact": {"تماس با کیپت", "برای راهنمایی، از مرکز پشتیبانی درخواست ثبت کنید. شمارهٔ رسمی تماس و فروشندهٔ ایرانی تماس صوتی/تصویری پیش از لانچ مشخص و اضافه خواهند شد."}, "magazine": {"مجلهٔ کیپت", "محتوای سلامت پس از بازبینی علمی دامپزشک منتشر خواهد شد. در نسخهٔ اولیه مقالهٔ پزشکی ساختگی یا بازبینی‌نشده منتشر نمی‌کنیم."}, "careers": {"فرصت‌های همکاری", "اگر دامپزشک، گرومر یا نمونه‌بردار هستید، از صفحهٔ همکاری با کیپت درخواست بدهید. فعال‌سازی حساب پس از بررسی صلاحیت توسط عملیات انجام می‌شود."}, "referral": {"معرفی دوستان", "برنامهٔ دعوت پس از تعیین قواعد و مبلغ پاداش فعال خواهد شد. فعلاً پاداش یا لینک درآمدزای صوری ایجاد نمی‌شود."}, "affiliate": {"همکاری در فروش", "شرایط کمیسیون و تسویه پس از نهایی شدن برنامهٔ همکاری اعلام خواهد شد."}}
	c, ok := content[slug]
	if !ok {
		a.fail(w, r, "صفحه پیدا نشد", 404)
		return
	}
	p := a.page(r, c[0])
	p.Content = c[1]
	a.render(w, r, "public", p)
}
