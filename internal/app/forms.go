package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"math"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
)

type FormView struct {
	Scope, Name string
	Questions   []Question
	Values      map[string]string
	Revision    int64
}

func textQ(id, label string, required bool) Question {
	return Question{ID: id, Label: label, Type: "text", Required: required}
}
func ratingQ(id, label string, required bool) Question {
	q := Question{ID: id, Label: label, Type: "radio", Required: required}
	for n := 1; n <= 5; n++ {
		q.Options = append(q.Options, QuestionOption{Value: strconv.Itoa(n), Label: digits(strconv.Itoa(n))})
	}
	return q
}
func (a *App) defaultForms() []FormView {
	pet := []Question{textQ("pet_name", "نام پت", true), {ID: "species", Label: "نوع حیوان", Type: "select", Required: true, Options: []QuestionOption{{"cat", "گربه"}, {"dog", "سگ"}}}, textQ("birth_year", "سال تولد شمسی (اگر نمی‌دانید خالی بگذارید)", false), textQ("age", "سن تقریبی (اگر سال تولد را نمی‌دانید الزامی است)", false), textQ("weight", "وزن تقریبی (کیلوگرم)", false)}
	owner := []Question{textQ("full_name", "نام و نام خانوادگی", true), {ID: "email", Label: "ایمیل", Type: "email"}, textQ("city", "شهر محل سکونت", false), textQ("emergency_phone", "شماره تماس جایگزین", false)}
	professional := []Question{textQ("education", "تحصیلات و دوره‌های آموزشی", false), textQ("experience", "سابقه کار (سال)", false), textQ("specialties", "تخصص‌ها و مهارت‌ها", false), {ID: "background", Label: "سوابق همکاری و مراکز قبلی", Type: "textarea"}, {ID: "email", Label: "ایمیل حرفه‌ای", Type: "email"}}
	forms := []FormView{{Scope: "pet", Name: "پروفایل پت", Questions: pet}, {Scope: "vet", Name: "شروع مشاوره دامپزشکی", Questions: a.Triage}, {Scope: "grooming", Name: "شروع گرومینگ", Questions: []Question{{ID: "behavior", Label: "خلق‌وخو و نکات مراقبت", Type: "textarea"}, {ID: "allergies", Label: "حساسیت‌ها و مشکلات پوستی", Type: "textarea"}, textQ("last_grooming", "آخرین زمان گرومینگ", false)}}, {Scope: "sampling", Name: "شروع نمونه‌برداری", Questions: []Question{textQ("lab_request", "نام آزمایش / درخواست دامپزشک", false), {ID: "medications", Label: "داروهای مصرفی و نکات مراقبت", Type: "textarea"}}}, {Scope: "signup-owner", Name: "ثبت‌نام مشتری", Questions: owner}, {Scope: "signup-vet", Name: "ثبت‌نام دامپزشک", Questions: professional}, {Scope: "signup-grooming", Name: "ثبت‌نام گرومر", Questions: professional}, {Scope: "signup-sampling", Name: "ثبت‌نام نمونه‌بردار", Questions: professional}, {Scope: "vet-report", Name: "گزارش پایان ویزیت", Questions: []Question{{ID: "symptoms", Label: "علائم کلی", Type: "textarea", Required: true}, {ID: "diagnosis", Label: "تشخیص", Type: "textarea", Required: true}, {ID: "prescription", Label: "تجویز و توصیه درمانی", Type: "textarea", Required: true}, ratingQ("customer_rating", "امتیاز به مشتری (اختیاری و محرمانه)", false), {ID: "customer_feedback", Label: "نظر درباره مشتری (اختیاری و محرمانه)", Type: "textarea"}}}, {Scope: "customer-survey", Name: "نظرسنجی پایان ویزیت", Questions: []Question{ratingQ("rating", "امتیاز کلی", true), ratingQ("communication", "کیفیت ارتباط", true), ratingQ("clarity", "وضوح توضیح‌ها", true), {ID: "body", Label: "نظر شما", Type: "textarea"}}}}
	for i := range forms {
		if forms[i].Scope == "vet-report" {
			for j := range forms[i].Questions {
				if contains([]string{"customer_rating", "customer_feedback"}, forms[i].Questions[j].ID) {
					forms[i].Questions[j].Private = true
				}
			}
		}
		if strings.HasPrefix(forms[i].Scope, "signup-") && forms[i].Scope != "signup-owner" {
			core := []Question{textQ("name", "نام و نام خانوادگی", true), textQ("license", "شماره مجوز / مدرک صلاحیت", forms[i].Scope == "signup-vet"), {ID: "price", Label: "تعرفه پیشنهادی (تومان)", Type: "number", Required: true}, {ID: "bio", Label: "معرفی و صلاحیت حرفه‌ای", Type: "textarea", Required: true}}
			if forms[i].Scope != "signup-vet" {
				core = append(core[:1], core[2:]...)
			}
			forms[i].Questions = append(core, forms[i].Questions...)
		}
	}
	forms = append(forms, FormView{Scope: "service-review", Name: "نظر و امتیاز خدمات", Questions: []Question{ratingQ("rating", "امتیاز خدمت", true), {ID: "body", Label: "نظر شما", Type: "textarea"}}}, FormView{Scope: "ticket", Name: "ثبت درخواست پشتیبانی", Questions: []Question{{ID: "subject", Label: "موضوع", Type: "select", Required: true, Options: []QuestionOption{{"پیگیری رزرو", "پیگیری رزرو"}, {"شکایت کیفیت خدمت", "شکایت کیفیت خدمت"}, {"درخواست پوشش محله", "درخواست پوشش محله"}, {"راهنمایی مراجعه به کلینیک / تماس", "راهنمایی مراجعه به کلینیک / تماس"}, {"سایر موارد", "سایر موارد"}}}, {ID: "email", Label: "ایمیل پیگیری", Type: "email", Required: true}, {ID: "body", Label: "توضیح شما", Type: "textarea", Required: true}}})
	return forms
}
func (a *App) seedForms(ctx context.Context) error {
	for _, f := range a.defaultForms() {
		if f.Scope == "vet" {
			f.Questions = a.questions(ctx)
		}
		raw, _ := json.Marshal(BusinessData{Name: f.Name, Questions: f.Questions, Enabled: true})
		if _, e := a.DB.Exec(ctx, "INSERT INTO business_config(kind,id,data) VALUES('form',$1,$2) ON CONFLICT DO NOTHING", f.Scope, raw); e != nil {
			return e
		}
	}
	for _, faq := range []BusinessRecord{{ID: "booking", Data: BusinessData{Name: "چطور رزرو کنم؟", Body: "خدمت، پت، متخصص و بازه را انتخاب کنید. عملیات زمان دقیق را هماهنگ می‌کند.", Enabled: true}}, {ID: "cancel", Data: BusinessData{Name: "چطور درخواست را لغو کنم؟", Body: "از جزئیات رزرو درخواست کنسلی ثبت کنید. سیاست بازگشت وجه در صفحه قوانین کنسلی آمده است.", Enabled: true}}, {ID: "biopsy", Data: BusinessData{Name: "نمونه برشی یا بیوپسی نیاز دارم", Body: "این خدمت فقط در کلینیک انجام می‌شود. برای راهنمایی، تیکت پشتیبانی ثبت کنید.", Enabled: true}}} {
		raw, _ := json.Marshal(faq.Data)
		if _, e := a.DB.Exec(ctx, "INSERT INTO business_config(kind,id,data) VALUES('faq',$1,$2) ON CONFLICT DO NOTHING", faq.ID, raw); e != nil {
			return e
		}
	}
	return a.seedEditorial(ctx)
}
func (a *App) form(ctx context.Context, scope string, values map[string]string) FormView {
	f := FormView{Scope: scope, Values: values}
	var raw []byte
	if a.DB.QueryRow(ctx, "SELECT data,revision FROM business_config WHERE kind='form' AND id=$1", scope).Scan(&raw, &f.Revision) == nil {
		var d BusinessData
		json.Unmarshal(raw, &d)
		f.Name = d.Name
		f.Questions = d.Questions
	}
	// Existing triage edits remain authoritative until migrated through the unified editor.
	if scope == "vet" {
		f.Questions = a.questions(ctx)
	}
	return f
}
func validFormQuestions(qs []Question) error {
	if len(qs) > 40 {
		return fmt.Errorf("حداکثر ۴۰ سؤال مجاز است")
	}
	seen := map[string]bool{}
	for _, q := range qs {
		if !businessID.MatchString(q.ID) || seen[q.ID] || strings.TrimSpace(q.Label) == "" || len(q.Label) > 500 || !contains([]string{"text", "textarea", "select", "radio", "checkbox", "number", "email", "date"}, q.Type) {
			return fmt.Errorf("شناسه، عنوان یا نوع سؤال نامعتبر است")
		}
		seen[q.ID] = true
		if contains([]string{"csrf", "step", "next", "service", "provider", "code", "terms", "consent", "sample", "area", "address", "pet_id", "id", "revision", "questions", "payment_key", "outcome"}, q.ID) {
			return fmt.Errorf("شناسه رزرو شده است")
		}
		if q.Type == "select" || q.Type == "radio" {
			if len(q.Options) == 0 || len(q.Options) > 30 {
				return fmt.Errorf("گزینه‌ها را تکمیل کنید")
			}
			opts := map[string]bool{}
			for _, o := range q.Options {
				if o.Value == "" || len(o.Value) > 100 || strings.ContainsAny(o.Value, "\r\n") || strings.TrimSpace(o.Label) == "" || len(o.Label) > 500 || opts[normalize(o.Value)] {
					return fmt.Errorf("گزینه نامعتبر یا تکراری")
				}
				opts[normalize(o.Value)] = true
			}
		}
		if q.When != "" {
			if q.ID == "symptom" {
				return fmt.Errorf("موضوع اصلی نباید شرط داشته باشد")
			}
			found := false
			for _, s := range qs {
				if s.ID == "symptom" {
					found = true
					for _, v := range strings.Split(q.When, ",") {
						ok := false
						for _, o := range s.Options {
							if o.Value == v {
								ok = true
							}
						}
						if !ok {
							return fmt.Errorf("شرط نمایش نامعتبر")
						}
					}
				}
			}
			if !found {
				return fmt.Errorf("شرط به موضوع اصلی نیاز دارد")
			}
		}
	}
	return nil
}
func formAnswers(r *http.Request, f FormView) (map[string]string, error) {
	out := map[string]string{}
	for _, q := range f.Questions {
		if q.When != "" && !contains(strings.Split(q.When, ","), r.FormValue("symptom")) {
			continue
		}
		v := strings.TrimSpace(r.FormValue(q.ID))
		if q.Required && v == "" {
			return out, fmt.Errorf("پاسخ «%s» لازم است", q.Label)
		}
		if len(v) > 8000 {
			return out, fmt.Errorf("پاسخ بیش از حد طولانی است")
		}
		if v != "" {
			switch q.Type {
			case "select", "radio":
				ok := false
				for _, o := range q.Options {
					if normalize(o.Value) == normalize(v) {
						ok = true
					}
				}
				if !ok {
					return out, fmt.Errorf("گزینه «%s» نامعتبر است", q.Label)
				}
			case "number":
				if n, e := strconv.ParseFloat(normalize(v), 64); e != nil || math.IsNaN(n) || math.IsInf(n, 0) {
					return out, fmt.Errorf("عدد معتبر وارد کنید")
				}
			case "email":
				if !validEmail(v) {
					return out, fmt.Errorf("ایمیل معتبر وارد کنید")
				}
			case "checkbox":
				if v != "on" {
					return out, fmt.Errorf("پاسخ نامعتبر")
				}
			case "date":
				if _, e := parseJalali(v); e != nil {
					return out, e
				}
			}
		}
		out[q.ID] = v
	}
	return out, nil
}
func validEmail(v string) bool {
	addr, e := mail.ParseAddress(v)
	return e == nil && addr.Address == v && len(v) <= 254
}
func saveFormResponse(ctx context.Context, tx pgx.Tx, f FormView, subject, user string, answers map[string]string) error {
	qs, _ := json.Marshal(f.Questions)
	vs, _ := json.Marshal(answers)
	_, e := tx.Exec(ctx, "INSERT INTO form_responses(scope,subject,user_id,questions,answers) VALUES($1,$2,$3,$4,$5) ON CONFLICT(scope,subject) DO UPDATE SET questions=excluded.questions,answers=excluded.answers", f.Scope, subject, user, qs, vs)
	return e
}
func (a *App) savedForm(ctx context.Context, scope, subject string) FormView {
	f := a.form(ctx, scope, map[string]string{})
	var qs, vs []byte
	if a.DB.QueryRow(ctx, "SELECT questions,answers FROM form_responses WHERE scope=$1 AND subject=$2", scope, subject).Scan(&qs, &vs) == nil {
		json.Unmarshal(qs, &f.Questions)
		json.Unmarshal(vs, &f.Values)
	}
	return f
}
func (a *App) formsAdmin(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	p := a.page(r, "مدیریت پرسشنامه‌ها")
	p.Forms = []FormView{}
	for _, d := range a.defaultForms() {
		p.Forms = append(p.Forms, a.form(r.Context(), d.Scope, nil))
	}
	a.render(w, r, "forms-admin", p)
}
func (a *App) saveQuestions(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	scope := r.PathValue("scope")
	known := false
	for _, f := range a.defaultForms() {
		if scope == f.Scope {
			known = true
		}
	}
	var qs []Question
	e := json.Unmarshal([]byte(r.FormValue("questions")), &qs)
	if !known || e != nil {
		a.fail(w, r, "پرسشنامه نامعتبر", 400)
		return
	}
	if e = validFormQuestions(qs); e != nil {
		a.fail(w, r, e.Error(), 400)
		return
	}
	if scope == "pet" {
		for _, q := range qs {
			if contains([]string{"age", "birth_year"}, q.ID) && q.Required {
				a.fail(w, r, "سال تولد و سن جایگزین هم هستند؛ الزام حداقل یکی خودکار بررسی می‌شود", 400)
				return
			}
		}
	}
	if scope != "pet" {
		for _, q := range qs {
			if contains([]string{"pet_name", "species", "age", "birth_year", "weight"}, q.ID) {
				a.fail(w, r, "شناسهٔ مشخصات پت فقط در پرسشنامهٔ پت استفاده می‌شود", 400)
				return
			}
		}
	}
	if scope == "vet-report" {
		for i := range qs {
			q := qs[i]
			if contains([]string{"customer_rating", "customer_feedback"}, q.ID) {
				qs[i].Private = true
			}
			if contains([]string{"customer_rating", "customer_feedback"}, q.ID) && q.Required {
				a.fail(w, r, "ارزیابی مشتری در گزارش دامپزشک باید اختیاری باشد", 400)
				return
			}
		}
	}
	core := map[string][]string{"pet": {"pet_name", "species", "age", "birth_year"}, "vet": {"symptom"}, "vet-report": {"symptoms", "diagnosis", "prescription"}, "customer-survey": {"rating", "communication", "clarity"}, "service-review": {"rating"}, "ticket": {"subject", "email", "body"}, "signup-vet": {"name", "bio", "price", "license"}, "signup-grooming": {"name", "bio", "price"}, "signup-sampling": {"name", "bio", "price"}}
	for _, id := range core[scope] {
		found := false
		for _, q := range qs {
			if q.ID == id {
				found = true
				if scope == "vet-report" || scope == "customer-survey" || scope == "service-review" || scope == "ticket" || strings.HasPrefix(scope, "signup-") || id == "symptom" || id == "pet_name" || id == "species" {
					if !q.Required {
						a.fail(w, r, "پاسخ‌های اصلی پرونده باید اجباری باشند", 400)
						return
					}
				}
			}
		}
		if !found {
			a.fail(w, r, "سؤال اصلی «"+id+"» باید حفظ شود؛ عنوان و نوع قابل تغییرند", 400)
			return
		}
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(r.Context())
	var raw []byte
	var rev int64
	e = tx.QueryRow(r.Context(), "SELECT data,revision FROM business_config WHERE kind='form' AND id=$1 FOR UPDATE", scope).Scan(&raw, &rev)
	expected, _ := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	if e != nil || rev != expected {
		a.fail(w, r, "این فرم تغییر کرده؛ صفحه را تازه کنید", 409)
		return
	}
	var d BusinessData
	json.Unmarshal(raw, &d)
	d.Questions = qs
	updated, _ := json.Marshal(d)
	_, e = tx.Exec(r.Context(), "UPDATE business_config SET data=$2,revision=revision+1 WHERE kind='form' AND id=$1", scope, updated)
	if e == nil && scope == "vet" {
		_, e = tx.Exec(r.Context(), "UPDATE business_config SET data=jsonb_set(data,'{questions}',$1),revision=revision+1 WHERE kind='triage' AND id='vet'", mustJSON(qs))
	}
	if e == nil {
		_, e = tx.Exec(r.Context(), "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,'form',$2,$3,$4)", sess(r).User.ID, scope, raw, updated)
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/management/questionnaires", "پرسشنامه ذخیره شد"))
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func (a *App) ownerProfile(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	p := a.page(r, "مشخصات حساب")
	saved := a.savedForm(r.Context(), "signup-owner", p.User.ID)
	if saved.Values == nil {
		saved.Values = map[string]string{}
	}
	if saved.Values["full_name"] == "" {
		saved.Values["full_name"] = p.User.Name
	}
	if saved.Values["email"] == "" {
		var email string
		if a.DB.QueryRow(r.Context(), "SELECT email FROM google_identities WHERE user_id=$1", p.User.ID).Scan(&email) == nil {
			saved.Values["email"] = email
		}
	}
	p.Form = a.form(r.Context(), "signup-owner", saved.Values)
	p.Next = safeNext(r.URL.Query().Get("next"))
	a.render(w, r, "owner-profile", p)
}
func (a *App) saveOwnerProfile(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	f := a.form(r.Context(), "signup-owner", nil)
	vs, e := formAnswers(r, f)
	if e != nil {
		a.fail(w, r, e.Error(), 400)
		return
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(r.Context())
	e = saveFormResponse(r.Context(), tx, f, sess(r).User.ID, sess(r).User.ID, vs)
	if e == nil {
		_, e = tx.Exec(r.Context(), "UPDATE users SET name=CASE WHEN $2='' THEN name ELSE $2 END WHERE id=$1", sess(r).User.ID, vs["full_name"])
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	redirect(w, r, safeNext(r.FormValue("next")))
}
