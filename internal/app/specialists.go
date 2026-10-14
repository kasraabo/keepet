package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

type ProfileMedia struct{ ID, Caption, Kind string }
type PublicReview struct {
	ID, Body, Source string
	Rating           int
	Published        bool
}
type SpecialistProfile struct {
	Provider                                     Provider
	Specialties, Education, Background, Portrait string
	Gallery                                      []ProfileMedia
	Reviews                                      []PublicReview
	Rating                                       string
	Count                                        int
}

func (a *App) specialist(ctx context.Context, id string) (*SpecialistProfile, error) {
	p := &SpecialistProfile{}
	e := a.DB.QueryRow(ctx, "SELECT id,COALESCE(user_id,''),name,service,bio,license,avatar,verified,active,species,areas,price,experience FROM providers WHERE id=$1", id).Scan(&p.Provider.ID, &p.Provider.UserID, &p.Provider.Name, &p.Provider.Service, &p.Provider.Bio, &p.Provider.License, &p.Provider.Avatar, &p.Provider.Verified, &p.Provider.Active, &p.Provider.Species, &p.Provider.Areas, &p.Provider.Price, &p.Provider.Experience)
	if e != nil {
		return nil, e
	}
	a.DB.QueryRow(ctx, "SELECT specialties,education,background FROM provider_profiles WHERE provider_id=$1", id).Scan(&p.Specialties, &p.Education, &p.Background)
	rows, e := a.DB.Query(ctx, "SELECT id,kind,caption FROM provider_media WHERE provider_id=$1 ORDER BY created_at", id)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var m ProfileMedia
		rows.Scan(&m.ID, &m.Kind, &m.Caption)
		if m.Kind == "portrait" {
			p.Portrait = m.ID
		} else {
			p.Gallery = append(p.Gallery, m)
		}
	}
	rows.Close()
	rows, e = a.DB.Query(ctx, `SELECT rating,body FROM reviews rv JOIN bookings b ON b.id=rv.booking_id WHERE b.provider_id=$1 AND rv.published AND b.service<>'vet' UNION ALL SELECT f.rating,f.body FROM consultation_customer_forms f JOIN consultation_chats c ON c.id=f.chat_id JOIN providers p ON p.user_id=c.vet_id WHERE p.id=$1 AND f.published`, id)
	if e != nil {
		return nil, e
	}
	sum := 0
	for rows.Next() {
		var v PublicReview
		rows.Scan(&v.Rating, &v.Body)
		sum += v.Rating
		p.Reviews = append(p.Reviews, v)
	}
	rows.Close()
	p.Count = len(p.Reviews)
	if p.Count > 0 {
		p.Rating = digits(fmt.Sprintf("%.1f", float64(sum)/float64(p.Count)))
	}
	return p, nil
}
func (a *App) specialistPage(w http.ResponseWriter, r *http.Request) {
	profile, e := a.specialist(r.Context(), r.PathValue("id"))
	if e != nil || !profile.Provider.Active || !profile.Provider.Verified || profile.Provider.Service == "sampling" {
		a.fail(w, r, "پروفایل در دسترس نیست", 404)
		return
	}
	p := a.page(r, profile.Provider.Name)
	p.Profile = profile
	a.render(w, r, "specialist", p)
}
func (a *App) specialistEditor(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "provider") {
		return
	}
	var id string
	if a.DB.QueryRow(r.Context(), "SELECT id FROM providers WHERE user_id=$1 AND service IN ('vet','grooming')", sess(r).User.ID).Scan(&id) != nil {
		a.fail(w, r, "پروفایل متخصص در دسترس نیست", 404)
		return
	}
	profile, e := a.specialist(r.Context(), id)
	if e != nil {
		a.fail(w, r, "بارگذاری نشد", 500)
		return
	}
	p := a.page(r, "ویرایش پروفایل حرفه‌ای")
	p.Profile = profile
	a.render(w, r, "specialist-editor", p)
}
func (a *App) saveSpecialist(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "provider") {
		return
	}
	name, bio := trimmed(r.FormValue("name")), trimmed(r.FormValue("bio"))
	xp, e := strconv.Atoi(normalize(r.FormValue("experience")))
	if name == "" || bio == "" || len(name) > 200 || len(bio) > 8000 || e != nil || xp < 0 || xp > 60 {
		a.fail(w, r, "نام، معرفی و سابقهٔ معتبر لازم است", 400)
		return
	}
	for _, key := range []string{"specialties", "education", "background"} {
		if len(r.FormValue(key)) > 8000 {
			a.fail(w, r, "متن بیش از حد طولانی است", 400)
			return
		}
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(r.Context())
	var id string
	e = tx.QueryRow(r.Context(), "UPDATE providers SET name=$2,bio=$3,experience=$4 WHERE user_id=$1 AND service IN ('vet','grooming') RETURNING id", sess(r).User.ID, name, bio, xp).Scan(&id)
	if e == nil {
		_, e = tx.Exec(r.Context(), "INSERT INTO provider_profiles(provider_id,specialties,education,background) VALUES($1,$2,$3,$4) ON CONFLICT(provider_id) DO UPDATE SET specialties=excluded.specialties,education=excluded.education,background=excluded.background", id, r.FormValue("specialties"), r.FormValue("education"), r.FormValue("background"))
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 400)
		return
	}
	redirect(w, r, noticeURL("/provider/profile", "پروفایل ذخیره شد"))
}
func (a *App) uploadSpecialistMedia(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "provider") {
		return
	}
	var id, svc string
	if a.DB.QueryRow(r.Context(), "SELECT id,service FROM providers WHERE user_id=$1 AND service IN ('vet','grooming')", sess(r).User.ID).Scan(&id, &svc) != nil {
		a.fail(w, r, "دسترسی ندارید", 403)
		return
	}
	kind := r.FormValue("kind")
	if kind != "portrait" && (kind != "gallery" || svc != "grooming") {
		a.fail(w, r, "نوع تصویر نامعتبر", 400)
		return
	}
	if len(r.FormValue("caption")) > 500 {
		a.fail(w, r, "عنوان طولانی است", 400)
		return
	}
	file, _, e := r.FormFile("image")
	if e != nil {
		a.fail(w, r, "تصویر را انتخاب کنید", 400)
		return
	}
	defer file.Close()
	raw, e := io.ReadAll(io.LimitReader(file, 8<<20+1))
	if e != nil || len(raw) > 8<<20 {
		a.fail(w, r, "حداکثر تصویر ۸ مگابایت", 400)
		return
	}
	mime := http.DetectContentType(raw)
	if !contains([]string{"image/jpeg", "image/png", "image/webp"}, mime) {
		a.fail(w, r, "فقط تصویر JPEG، PNG یا WebP", 400)
		return
	}
	if e = validateProfileImage(raw); e != nil {
		a.fail(w, r, e.Error(), 400)
		return
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(r.Context())
	var locked string
	e = tx.QueryRow(r.Context(), "SELECT id FROM providers WHERE id=$1 FOR UPDATE", id).Scan(&locked)
	if e == nil && kind == "portrait" {
		_, e = tx.Exec(r.Context(), "DELETE FROM provider_media WHERE provider_id=$1 AND kind='portrait'", id)
	}
	var count int
	if e == nil {
		e = tx.QueryRow(r.Context(), "SELECT count(*) FROM provider_media WHERE provider_id=$1", id).Scan(&count)
	}
	if count >= 21 {
		a.fail(w, r, "حداکثر ۲۰ نمونه‌کار مجاز است", 400)
		return
	}
	if e == nil {
		_, e = tx.Exec(r.Context(), "INSERT INTO provider_media(id,provider_id,kind,caption,mime,data) VALUES($1,$2,$3,$4,$5,$6)", token(), id, kind, r.FormValue("caption"), mime, raw)
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		a.fail(w, r, "تصویر ذخیره نشد", 500)
		return
	}
	redirect(w, r, "/provider/profile")
}
func (a *App) deleteSpecialistMedia(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "provider") {
		return
	}
	_, e := a.DB.Exec(r.Context(), "DELETE FROM provider_media m USING providers p WHERE m.provider_id=p.id AND p.user_id=$1 AND m.id=$2", sess(r).User.ID, r.PathValue("id"))
	if e != nil {
		a.fail(w, r, "حذف نشد", 500)
		return
	}
	redirect(w, r, "/provider/profile")
}
func (a *App) specialistMedia(w http.ResponseWriter, r *http.Request) {
	var mime string
	var raw []byte
	var owner string
	var active bool
	e := a.DB.QueryRow(r.Context(), "SELECT m.mime,m.data,COALESCE(p.user_id,''),p.active AND p.verified FROM provider_media m JOIN providers p ON p.id=m.provider_id WHERE m.id=$1", r.PathValue("id")).Scan(&mime, &raw, &owner, &active)
	if e != nil || !active && (sess(r).User == nil || sess(r).User.ID != owner && sess(r).User.Role != "admin") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Write(raw)
}
func (a *App) reviewsAdmin(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	p := a.page(r, "مدیریت نظرهای عمومی")
	p.Profile = &SpecialistProfile{}
	rows, e := a.DB.Query(r.Context(), `SELECT rv.booking_id,rv.body,rv.rating,rv.published,'review' FROM reviews rv JOIN bookings b ON b.id=rv.booking_id WHERE b.service<>'vet' UNION ALL SELECT chat_id,body,rating,published,'chat' FROM consultation_customer_forms`)
	if e != nil {
		a.fail(w, r, "بارگذاری نشد", 500)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var v PublicReview
		rows.Scan(&v.ID, &v.Body, &v.Rating, &v.Published, &v.Source)
		p.Profile.Reviews = append(p.Profile.Reviews, v)
	}
	a.render(w, r, "reviews-admin", p)
}
func (a *App) publishReview(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin") {
		return
	}
	sql := "UPDATE reviews SET published=$2 WHERE booking_id=$1"
	if r.FormValue("source") == "chat" {
		sql = "UPDATE consultation_customer_forms SET published=$2 WHERE chat_id=$1"
	}
	_, e := a.DB.Exec(r.Context(), sql, r.PathValue("id"), r.FormValue("published") == "on")
	if e != nil {
		a.fail(w, r, "ثبت نشد", 500)
		return
	}
	redirect(w, r, "/management/reviews")
}
