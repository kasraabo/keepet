package app

import (
	"net/http"
	"strconv"
	"time"
)

type Notification struct {
	ID                                                      int64
	Kind, Event, BookingID, ChatID, Title, Link, FollowupID string
	At                                                      time.Time
	ReadAt                                                  *time.Time
}
type NotificationPage struct {
	Items  []Notification
	Before int64
}

const notificationAccess = "(n.booking_id IS NULL OR b.owner_id=$1 OR EXISTS(SELECT 1 FROM providers p WHERE p.id=b.provider_id AND p.user_id=$1) OR $2 IN ('ops','admin')) AND (n.kind<>'operations_event' OR $2 IN ('ops','admin'))"

func (a *App) notificationCount(r *http.Request) int {
	u := sess(r).User
	if u == nil {
		return 0
	}
	var n int
	a.DB.QueryRow(r.Context(), "SELECT count(*) FROM notifications n LEFT JOIN bookings b ON b.id=n.booking_id WHERE n.user_id=$1 AND n.read_at IS NULL AND "+notificationAccess, u.ID, u.Role).Scan(&n)
	return n
}
func (a *App) notificationsCount(w http.ResponseWriter, r *http.Request) {
	if sess(r).User == nil {
		chatJSON(w, 401, map[string]string{"error": "ابتدا وارد شوید"})
		return
	}
	chatJSON(w, 200, map[string]int{"unread": a.notificationCount(r)})
}
func notificationText(n *Notification, role string) {
	if n.Kind == "care_followup" {
		n.Title = n.Event
		n.Link = "/follow-ups/" + n.FollowupID
		return
	}
	if n.Kind == "chat_message" {
		n.Title = "پیام جدید در گفت‌وگوی سفارش"
		n.Link = "/chats/" + n.ChatID
		return
	}
	label := status(n.Event)
	if label == n.Event {
		label = "تغییر وضعیت سفارش"
	}
	n.Title = label
	n.Link = "/bookings/" + n.BookingID
	if role == "ops" || role == "admin" {
		n.Link = "/management/operations?booking=" + n.BookingID
	} else if role == "provider" {
		n.Link = "/provider"
	}
	if contains([]string{"arrival_requested", "attendance_verified", "service_reported_done", "handover_uploaded", "handover_approved", "handover_rejected", "service_issue", "service_issue_resolved"}, n.Event) {
		n.Link = "/bookings/" + n.BookingID + "/fulfillment"
	}
	if n.Event == "result_ready" {
		n.Link = "/bookings/" + n.BookingID + "/laboratory"
	}
}
func (a *App) notificationsPage(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	p := a.page(r, "اعلان‌های من")
	v := &NotificationPage{}
	p.Notifications = v
	u := sess(r).User
	rows, e := a.DB.Query(r.Context(), "SELECT n.id,n.kind,n.event,COALESCE(n.booking_id,''),COALESCE(n.chat_id,''),n.created_at,n.read_at,COALESCE(n.followup_id,'') FROM notifications n LEFT JOIN bookings b ON b.id=n.booking_id WHERE n.user_id=$1 AND "+notificationAccess+" ORDER BY n.id DESC LIMIT 100", u.ID, u.Role)
	if e != nil {
		a.fail(w, r, "اعلان‌ها بارگذاری نشد", 500)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var n Notification
		if e = rows.Scan(&n.ID, &n.Kind, &n.Event, &n.BookingID, &n.ChatID, &n.At, &n.ReadAt, &n.FollowupID); e != nil {
			break
		}
		notificationText(&n, u.Role)
		if n.ID > v.Before {
			v.Before = n.ID
		}
		v.Items = append(v.Items, n)
	}
	if e == nil {
		e = rows.Err()
	}
	if e != nil {
		a.fail(w, r, "اعلان‌ها بارگذاری نشد", 500)
		return
	}
	a.render(w, r, "notifications", p)
}
func (a *App) notificationRead(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	u := sess(r).User
	if r.PathValue("id") == "all" {
		max, e := strconv.ParseInt(r.FormValue("before_id"), 10, 64)
		if e != nil || max < 0 {
			a.fail(w, r, "نسخهٔ فهرست معتبر نیست", 400)
			return
		}
		_, e = a.DB.Exec(r.Context(), "UPDATE notifications SET read_at=COALESCE(read_at,now()) WHERE user_id=$1 AND id<=$2", u.ID, max)
		if e != nil {
			a.fail(w, r, "ثبت نشد", 500)
			return
		}
		redirect(w, r, "/notifications")
		return
	}
	id, e := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if e != nil {
		a.fail(w, r, "اعلان پیدا نشد", 404)
		return
	}
	var n Notification
	e = a.DB.QueryRow(r.Context(), "UPDATE notifications n SET read_at=COALESCE(n.read_at,now()) WHERE n.id=$3 AND n.user_id=$1 AND EXISTS(SELECT 1 FROM notifications n LEFT JOIN bookings b ON b.id=n.booking_id WHERE n.id=$3 AND n.user_id=$1 AND "+notificationAccess+") RETURNING n.kind,n.event,COALESCE(n.booking_id,''),COALESCE(n.chat_id,''),COALESCE(n.followup_id,'')", u.ID, u.Role, id).Scan(&n.Kind, &n.Event, &n.BookingID, &n.ChatID, &n.FollowupID)
	if e != nil {
		a.fail(w, r, "اعلان پیدا نشد", 404)
		return
	}
	notificationText(&n, u.Role)
	redirect(w, r, n.Link)
}
func (a *App) chatRead(w http.ResponseWriter, r *http.Request) {
	if sess(r).User == nil {
		a.chatFailure(w, r, "ابتدا وارد شوید", 401)
		return
	}
	last, e := strconv.ParseInt(normalize(r.FormValue("last_message_id")), 10, 64)
	if e != nil || last < 1 {
		a.chatFailure(w, r, "پیام معتبر انتخاب کنید", 400)
		return
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.chatFailure(w, r, "ثبت نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	c, role, e := a.lockedChat(ctx, tx, r.PathValue("id"), sess(r).User)
	if e != nil {
		a.chatFailure(w, r, "گفتگو در دسترس نیست", 404)
		return
	}
	if role == "admin" {
		a.chatFailure(w, r, "وضعیت خواندن مخصوص شرکت‌کنندگان است", 403)
		return
	}
	var valid bool
	e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM consultation_messages WHERE chat_id=$1 AND id=$2)", c.ID, last).Scan(&valid)
	if e != nil || !valid {
		a.chatFailure(w, r, "پیام متعلق به این گفتگو نیست", 400)
		return
	}
	uid := sess(r).User.ID
	_, e = tx.Exec(ctx, "INSERT INTO chat_reads(chat_id,user_id,last_message_id) VALUES($1,$2,$3) ON CONFLICT(chat_id,user_id) DO UPDATE SET last_message_id=GREATEST(chat_reads.last_message_id,EXCLUDED.last_message_id),updated_at=now()", c.ID, uid, last)
	if e == nil {
		_, e = tx.Exec(ctx, "UPDATE notifications SET read_at=COALESCE(read_at,now()) WHERE user_id=$1 AND chat_id=$2 AND message_id<=$3", uid, c.ID, last)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.chatFailure(w, r, "وضعیت خواندن ثبت نشد", 500)
		return
	}
	chatJSON(w, 200, map[string]bool{"ok": true})
}
