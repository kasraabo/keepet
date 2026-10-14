package app

import (
	"net/http"
	"strconv"
)

func (a *App) chatForms(r *http.Request, p *Page) {
	if p.Chat == nil {
		return
	}
	c := p.Chat
	scope := "customer-survey"
	if c.Role == "vet" {
		scope = "vet-report"
	}
	p.Form = a.savedForm(r.Context(), scope, c.ID)
	if !c.QuestionnaireDone || c.Role == "vet" && c.State == "active" {
		p.Form = a.form(r.Context(), scope, p.Form.Values)
	}
	if p.Form.Values == nil {
		p.Form.Values = map[string]string{}
	}
	if c.Role == "vet" && c.VetForm != nil {
		f := c.VetForm
		p.Form.Values["symptoms"] = f.Symptoms
		p.Form.Values["diagnosis"] = f.Diagnosis
		p.Form.Values["prescription"] = f.Prescription
		p.Form.Values["customer_feedback"] = f.CustomerFeedback
		if f.CustomerRating > 0 {
			p.Form.Values["customer_rating"] = strconv.Itoa(f.CustomerRating)
		}
	}
	if c.Role == "owner" && c.CustomerForm != nil {
		f := c.CustomerForm
		p.Form.Values["rating"] = strconv.Itoa(f.Rating)
		p.Form.Values["communication"] = strconv.Itoa(f.Communication)
		p.Form.Values["clarity"] = strconv.Itoa(f.Clarity)
		p.Form.Values["body"] = f.Body
	}
	if r.Method == "POST" {
		for _, q := range p.Form.Questions {
			p.Form.Values[q.ID] = r.FormValue(q.ID)
		}
	}
	for _, s := range []string{"vet-report", "customer-survey"} {
		if c.Role == "admin" || c.Role == "vet" && s == "vet-report" || c.Role == "owner" {
			f := a.savedForm(r.Context(), s, c.ID)
			if c.Role == "owner" && s == "vet-report" {
				visible := []Question{}
				for _, q := range f.Questions {
					if !q.Private && !contains([]string{"customer_rating", "customer_feedback"}, q.ID) {
						visible = append(visible, q)
					} else {
						delete(f.Values, q.ID)
					}
				}
				f.Questions = visible
			}
			if len(f.Values) > 0 {
				p.Forms = append(p.Forms, f)
			}
		}
	}
}
