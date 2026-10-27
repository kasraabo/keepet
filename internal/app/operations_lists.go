package app

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
)

type OpsPager struct {
	Page, Count, Pages int
	Previous, Next     string
}
type OperationsView struct {
	Query, Service, Status, ApplicationStatus, TicketStatus, Sort string
	Bookings, Applications, Tickets                               OpsPager
}

var errOperationsFilters = errors.New("جست‌وجو یا فیلتر عملیات معتبر نیست")
var opsStatuses = []string{"pending_ops", "pending_provider", "awaiting_payment", "paid", "confirmed", "assigned", "accepted", "in_progress", "awaiting_customer", "awaiting_report", "collected", "delivery_pending", "delivered", "result_ready", "completed", "cancel_requested", "cancelled", "replacement_pending", "request_expired", "request_rejected", "request_cancelled"}

func opsPager(r *http.Request, key, anchor string, total int) OpsPager {
	page, _ := strconv.Atoi(r.URL.Query().Get(key))
	if page < 1 {
		page = 1
	}
	pages := (total + 24) / 25
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	p := OpsPager{Page: page, Count: total, Pages: pages}
	link := func(n int) string {
		q := r.URL.Query()
		q.Set(key, strconv.Itoa(n))
		return r.URL.Path + "?" + q.Encode() + "#" + anchor
	}
	if page > 1 {
		p.Previous = link(page - 1)
	}
	if page < pages {
		p.Next = link(page + 1)
	}
	return p
}
func likeSearch(s string) string {
	return "%" + strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(s) + "%"
}
func (a *App) loadOperations(r *http.Request, p *Page) error {
	ctx := r.Context()
	q := r.URL.Query()
	v := &OperationsView{Query: trimmed(q.Get("q")), Service: q.Get("service"), Status: q.Get("status"), ApplicationStatus: q.Get("application_status"), TicketStatus: q.Get("ticket_status"), Sort: q.Get("sort")}
	p.OpsView = v
	if len(v.Query) > 400 || (v.Service != "" && service(v.Service).ID == "") || (v.Status != "" && !contains(opsStatuses, v.Status)) || !contains([]string{"", "pending", "approved", "rejected"}, v.ApplicationStatus) || !contains([]string{"", "open", "closed"}, v.TicketStatus) || !contains([]string{"", "priority", "newest", "oldest"}, v.Sort) {
		return errOperationsFilters
	}
	pattern := likeSearch(normalize(v.Query))
	target := q.Get("booking")
	where := `((payment_status='paid' OR status='pending_ops' OR (request_flow AND status<>'draft')) OR ($1<>'' AND id=$1)) AND ($1='' OR id=$1) AND ($2='' OR service=$2) AND ($3='' OR status=$3) AND ($4='%%' OR id ILIKE $4 OR pet_name ILIKE $4 OR EXISTS(SELECT 1 FROM users u WHERE u.id=bookings.owner_id AND (u.name ILIKE $4 OR u.phone ILIKE $4)) OR EXISTS(SELECT 1 FROM providers p WHERE p.id=bookings.provider_id AND p.name ILIKE $4))`
	args := []any{target, v.Service, v.Status, pattern}
	var total int
	if e := a.DB.QueryRow(ctx, "SELECT count(*) FROM bookings WHERE "+where, args...).Scan(&total); e != nil {
		return e
	}
	v.Bookings = opsPager(r, "page", "queue", total)
	order := `CASE WHEN status='pending_ops' THEN 0 WHEN status IN ('cancel_requested','replacement_pending','delivery_pending') THEN 1 WHEN status IN ('paid','confirmed') THEN 2 ELSE 3 END,created_at DESC,id DESC`
	if v.Sort == "newest" {
		order = "created_at DESC,id DESC"
	}
	if v.Sort == "oldest" {
		order = "created_at,id"
	}
	args = append(args, (v.Bookings.Page-1)*25)
	rows, e := a.DB.Query(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE "+where+" ORDER BY "+order+" LIMIT 25 OFFSET $5", args...)
	if e != nil {
		return e
	}
	for rows.Next() {
		b, err := scanBooking(rows)
		if err != nil {
			rows.Close()
			return err
		}
		p.Bookings = append(p.Bookings, b)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	where = `($1='' OR service=$1) AND ($2='' OR status=$2) AND ($3='%%' OR name ILIKE $3 OR id ILIKE $3 OR EXISTS(SELECT 1 FROM users u WHERE u.id=applications.user_id AND (u.phone ILIKE $3 OR u.name ILIKE $3)))`
	args = []any{v.Service, v.ApplicationStatus, pattern}
	if e = a.DB.QueryRow(ctx, "SELECT count(*) FROM applications WHERE "+where, args...).Scan(&total); e != nil {
		return e
	}
	v.Applications = opsPager(r, "application_page", "applications", total)
	args = append(args, (v.Applications.Page-1)*25)
	rows, e = a.DB.Query(ctx, "SELECT id,user_id,service,name,license,bio,area,price,status,reason FROM applications WHERE "+where+" ORDER BY created_at DESC,id DESC LIMIT 25 OFFSET $4", args...)
	if e != nil {
		return e
	}
	for rows.Next() {
		var ap Application
		if e = rows.Scan(&ap.ID, &ap.UserID, &ap.Service, &ap.Name, &ap.License, &ap.Bio, &ap.Area, &ap.Price, &ap.Status, &ap.Reason); e != nil {
			rows.Close()
			return e
		}
		p.Applications = append(p.Applications, ap)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	where = `($1='' OR status=$1) AND ($2='%%' OR subject ILIKE $2 OR id ILIKE $2 OR email ILIKE $2 OR EXISTS(SELECT 1 FROM users u WHERE u.id=tickets.user_id AND (u.phone ILIKE $2 OR u.name ILIKE $2)))`
	args = []any{v.TicketStatus, pattern}
	if e = a.DB.QueryRow(ctx, "SELECT count(*) FROM tickets WHERE "+where, args...).Scan(&total); e != nil {
		return e
	}
	v.Tickets = opsPager(r, "ticket_page", "tickets", total)
	args = append(args, (v.Tickets.Page-1)*25)
	rows, e = a.DB.Query(ctx, "SELECT id,subject,body,reply,status,email FROM tickets WHERE "+where+" ORDER BY created_at DESC,id DESC LIMIT 25 OFFSET $3", args...)
	if e != nil {
		return e
	}
	for rows.Next() {
		var t Ticket
		if e = rows.Scan(&t.ID, &t.Subject, &t.Body, &t.Reply, &t.Status, &t.Email); e != nil {
			rows.Close()
			return e
		}
		p.Tickets = append(p.Tickets, t)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for i := range p.Tickets {
		p.Tickets[i].Form = a.savedForm(ctx, "ticket", p.Tickets[i].ID)
		extra := []Question{}
		for _, q := range p.Tickets[i].Form.Questions {
			if !contains([]string{"subject", "email", "body"}, q.ID) {
				extra = append(extra, q)
			}
		}
		p.Tickets[i].Form.Questions = extra
	}
	return nil
}
