package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

func (a *App) pets(ctx context.Context, user string) []map[string]string {
	rows, e := a.DB.Query(ctx, "SELECT id,name,species,age,weight,birth_year FROM pets WHERE user_id=$1 ORDER BY name", user)
	if e != nil {
		return nil
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var id, name, sp, age, weight, year string
		if rows.Scan(&id, &name, &sp, &age, &weight, &year) == nil {
			out = append(out, map[string]string{"id": id, "name": name, "pet_name": name, "species": sp, "age": age, "weight": weight, "birth_year": year})
		}
	}
	return out
}
func (a *App) bookingPet(w http.ResponseWriter, r *http.Request) {
	b, e := a.getBooking(r)
	if e != nil || !owned(r, b) || b.Status != "draft" || b.Stage != 1 {
		a.fail(w, r, "درخواست در دسترس نیست", 404)
		return
	}
	id := r.FormValue("pet_id")
	vs := map[string]string{}
	if id != "" {
		if sess(r).User == nil {
			a.fail(w, r, "وارد حساب شوید", 403)
			return
		}
		found := false
		for _, p := range a.pets(r.Context(), sess(r).User.ID) {
			if p["id"] == id {
				vs = p
				found = true
			}
		}
		if !found {
			a.fail(w, r, "پت در دسترس نیست", 404)
			return
		}
	}
	answers := b.Answers
	if answers == nil {
		answers = map[string]string{}
	}
	answers["_pet_id"] = id
	answers["birth_year"] = vs["birth_year"]
	tag, e := a.DB.Exec(r.Context(), "UPDATE bookings SET pet_name=$2,species=$3,age=$4,weight=$5,answers=$6 WHERE id=$1 AND status='draft' AND stage=1", b.ID, vs["pet_name"], func() string {
		if vs["species"] == "" {
			return "cat"
		}
		return vs["species"]
	}(), vs["age"], vs["weight"], mustJSON(answers))
	if e != nil {
		a.fail(w, r, "انتخاب پت ثبت نشد", 500)
		return
	}
	if tag.RowsAffected() == 0 {
		a.fail(w, r, "مرحلهٔ درخواست تغییر کرده؛ صفحه را تازه کنید.", 409)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		fresh, err := a.getBooking(r)
		if err != nil {
			a.fail(w, r, "اطلاعات پت بارگذاری نشد", 500)
			return
		}
		a.renderFragment(w, r, "booking-pet-fields", "#booking-pet-fields", a.bookingData(r, fresh, ""))
		return
	}
	redirect(w, r, "/booking/"+b.ID)
}
func (a *App) stepPet(r *http.Request, b *Booking) error {
	if id := b.Answers["_pet_id"]; id != "" {
		found := false
		if sess(r).User != nil {
			for _, p := range a.pets(r.Context(), sess(r).User.ID) {
				if p["id"] == id {
					b.PetName = p["name"]
					b.Species = p["species"]
					if p["age"] != "" {
						b.Age = p["age"]
					}
					b.Weight = p["weight"]
					if p["birth_year"] != "" {
						r.Form.Set("birth_year", p["birth_year"])
					}
					stored := a.savedForm(r.Context(), "pet", id)
					for _, q := range a.form(r.Context(), "pet", nil).Questions {
						if !contains([]string{"pet_name", "species", "birth_year", "age", "weight"}, q.ID) && reusePetAnswer(q, stored) {
							r.Form.Set(q.ID, stored.Values[q.ID])
						}
					}
					found = true
					break
				}
			}
		}
		if !found {
			return http.ErrNoCookie
		}
		r.Form.Set("pet_name", b.PetName)
		r.Form.Set("species", b.Species)
		r.Form.Set("age", b.Age)
		r.Form.Set("weight", b.Weight)
	}
	age, year, e := petBirth(b.Age, r.FormValue("birth_year"))
	if e != nil {
		return e
	}
	b.Age = age
	r.Form.Set("age", age)
	r.Form.Set("birth_year", year)
	return nil
}
func petValues(b Booking) map[string]string {
	return map[string]string{"pet_name": b.PetName, "species": b.Species, "age": b.Age, "weight": b.Weight, "birth_year": b.Answers["birth_year"]}
}
func trimmed(s string) string { return strings.TrimSpace(s) }
func bookingAnswers(b Booking) FormView {
	f := FormView{Values: b.Answers}
	json.Unmarshal([]byte(b.Answers["_questions"]), &f.Questions)
	var pet []Question
	var vs map[string]string
	json.Unmarshal([]byte(b.Answers["_pet_questions"]), &pet)
	json.Unmarshal([]byte(b.Answers["_pet_answers"]), &vs)
	f.Values = map[string]string{}
	for k, v := range vs {
		f.Values[k] = v
	}
	for k, v := range b.Answers {
		f.Values[k] = v
	}
	f.Questions = append(pet, f.Questions...)
	if len(f.Questions) == 0 {
		for k, v := range b.Answers {
			if v != "" && !strings.HasPrefix(k, "_") {
				f.Questions = append(f.Questions, Question{ID: k, Label: k})
			}
		}
	}
	return f
}
func reusePetAnswer(q Question, f FormView) bool {
	v := f.Values[q.ID]
	if v == "" {
		return false
	}
	for _, old := range f.Questions {
		if old.ID == q.ID && old.Type != q.Type {
			return false
		}
	}
	if q.Type == "select" || q.Type == "radio" {
		for _, o := range q.Options {
			if normalize(o.Value) == normalize(v) {
				return true
			}
		}
		return false
	}
	return true
}
