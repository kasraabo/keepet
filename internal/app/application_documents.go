package app

import (
	"fmt"
	"net/http"
	"strings"
)

type ApplicationDocument struct{ ID, Filename, Label string }

func (a *App) applicationDocuments(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	p := a.page(r, "مدارک درخواست همکاری")
	var ap Application
	e := a.DB.QueryRow(r.Context(), "SELECT id,user_id,service,name,status FROM applications WHERE id=$1", r.PathValue("id")).Scan(&ap.ID, &ap.UserID, &ap.Service, &ap.Name, &ap.Status)
	if e != nil {
		a.fail(w, r, "درخواست پیدا نشد", 404)
		return
	}
	if ap.UserID != p.User.ID && !contains([]string{"ops", "admin"}, p.User.Role) {
		a.fail(w, r, "به مدارک این درخواست دسترسی ندارید", 403)
		return
	}
	rows, e := a.DB.Query(r.Context(), "SELECT id,filename,label FROM application_documents WHERE application_id=$1 ORDER BY created_at", ap.ID)
	if e != nil {
		a.fail(w, r, "مدارک بارگذاری نشدند", 500)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var d ApplicationDocument
		if e = rows.Scan(&d.ID, &d.Filename, &d.Label); e != nil {
			a.fail(w, r, "مدارک خوانده نشدند", 500)
			return
		}
		ap.Documents = append(ap.Documents, d)
	}
	if rows.Err() != nil {
		a.fail(w, r, "مدارک خوانده نشدند", 500)
		return
	}
	p.Applications = []Application{ap}
	a.render(w, r, "application-documents", p)
}
func (a *App) uploadApplicationDocument(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "owner", "provider") {
		return
	}
	// Reuse the bounded PDF/image decoder; private files never enter the public gallery.
	label := r.FormValue("document_type")
	if !contains([]string{"کارت نظام دامپزشکی", "مدرک تحصیلی", "سایر مدارک صلاحیت"}, label) {
		a.fail(w, r, "نوع مدرک را انتخاب کنید", 400)
		return
	}
	raw, mime, name, e := readHandover(r)
	if e != nil {
		a.fail(w, r, "مدرک بارگذاری نشد: "+strings.ReplaceAll(e.Error(), "رسید", "مدرک"), 400)
		return
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "مدرک ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	var owner, svc, status string
	e = tx.QueryRow(ctx, "SELECT user_id,service,status FROM applications WHERE id=$1 FOR UPDATE", r.PathValue("id")).Scan(&owner, &svc, &status)
	if e != nil || owner != sess(r).User.ID {
		a.fail(w, r, "به درخواست دسترسی ندارید", 403)
		return
	}
	if svc != "vet" || status != "pending" {
		a.fail(w, r, "مدارک فقط برای درخواست دامپزشکی در انتظار بررسی قابل افزودن است", 409)
		return
	}
	var n int
	e = tx.QueryRow(ctx, "SELECT count(*) FROM application_documents WHERE application_id=$1", r.PathValue("id")).Scan(&n)
	if e != nil {
		a.fail(w, r, "مدارک بررسی نشدند", 500)
		return
	}
	if n >= 5 {
		a.fail(w, r, "حداکثر پنج مدرک مجاز است؛ برای اصلاح با پشتیبانی تماس بگیرید", 409)
		return
	}
	ext := "jpg"
	if mime == "application/pdf" {
		ext = "pdf"
	}
	name = fmt.Sprintf("document-%d.%s", n+1, ext)
	_, e = tx.Exec(ctx, "INSERT INTO application_documents(id,application_id,filename,mime,data,label) VALUES($1,$2,$3,$4,$5,$6)", token(), r.PathValue("id"), name, mime, raw, label)
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "مدرک ذخیره نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/applications/"+r.PathValue("id")+"/documents", "مدرک با موفقیت بارگذاری شد و برای بررسی عملیات در دسترس است."))
}
func (a *App) applicationDocumentDownload(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	var owner, mime, name string
	var data []byte
	e := a.DB.QueryRow(r.Context(), "SELECT ap.user_id,d.mime,d.filename,d.data FROM application_documents d JOIN applications ap ON ap.id=d.application_id WHERE ap.id=$1 AND d.id=$2", r.PathValue("id"), r.PathValue("document")).Scan(&owner, &mime, &name, &data)
	if e != nil {
		a.fail(w, r, "مدرک پیدا نشد", 404)
		return
	}
	if owner != sess(r).User.ID && !contains([]string{"ops", "admin"}, sess(r).User.Role) {
		a.fail(w, r, "به مدرک دسترسی ندارید", 403)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(data)
}
