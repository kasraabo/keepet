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
	content := publicPages
	c, ok := content[slug]
	if !ok {
		a.fail(w, r, "صفحه پیدا نشد", 404)
		return
	}
	p := a.page(r, c[0])
	p.Content = c[1]
	a.render(w, r, "public", p)
}
