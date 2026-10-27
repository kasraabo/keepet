package app

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Notification struct {
	ID                                                                              int64
	Kind, Event, BookingID, ChatID, Title, Link, FollowupID, SourceID, SourceUserID string
	At                                                                              time.Time
	ReadAt                                                                          *time.Time
}
type NotificationPage struct {
	Items  []Notification
	Before int64
}

const notificationAccess = "(n.booking_id IS NULL OR b.owner_id=$1 OR EXISTS(SELECT 1 FROM providers p WHERE p.id=b.provider_id AND p.user_id=$1) OR $2 IN ('ops','admin')) AND (n.kind NOT IN ('operations_event','wallet_operations','ticket_operations') OR $2 IN ('ops','admin')) AND (n.kind<>'editorial' OR $2 IN ('admin','content')) AND (n.kind NOT IN ('wallet_entry','wallet_account','bank_account','withdrawal','ticket') OR n.source_user_id=$1)"

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
	switch n.Kind {
	case "wallet_account":
		n.Title = "محدودیت کیف پول برداشته شد"
		if n.Event == "frozen" {
			n.Title = "کیف پول محدود شد؛ از پشتیبانی پیگیری کنید"
		}
		n.Link = "/wallet"
		return
	case "wallet_entry":
		n.Title = "تراکنش جدید در کیف پول"
		labels := map[string]string{"service_income": "درآمد خدمت به کیف پول اضافه شد", "refund": "وجه رزرو به کیف پول بازگشت", "referral_income": "درآمد معرفی به کیف پول اضافه شد", "referral_reward": "پاداش معرفی ثبت شد", "referral_welcome": "اعتبار خوش‌آمدگویی ثبت شد", "referral_signup": "پاداش عضویت فرد معرفی‌شده ثبت شد", "referral_recovery": "اصلاح پاداش معرفی ثبت شد", "admin_cash": "موجودی نقدی کیف پول اصلاح شد", "admin_bonus": "اعتبار هدیهٔ کیف پول اصلاح شد", "booking_payment": "پرداخت رزرو از کیف پول ثبت شد", "topup_demo": "شارژ آزمایشی کیف پول ثبت شد"}
		if v := labels[n.Event]; v != "" {
			n.Title = v
		}
		n.Link = "/wallet?entry=" + url.QueryEscape(n.SourceID) + "#history"
		return
	case "bank_account":
		n.Title = map[string]string{"pending": "شبا در انتظار بررسی مدیریت است", "verified": "شبای شما تأیید شد", "rejected": "شبا تأیید نشد؛ توضیح مدیریت را بررسی کنید"}[n.Event]
		n.Link = "/wallet#bank"
		return
	case "withdrawal":
		n.Title = map[string]string{"pending": "درخواست برداشت ثبت شد", "processing": "درخواست برداشت در حال پرداخت است", "paid": "رسید پرداخت برداشت ثبت شد", "rejected": "درخواست برداشت رد و مبلغ آزاد شد", "cancelled": "برداشت لغو و مبلغ آزاد شد"}[n.Event]
		n.Link = "/wallet?withdrawal=" + url.QueryEscape(n.SourceID) + "#withdrawals"
		return
	case "wallet_operations":
		n.Title = "درخواست برداشت نیازمند رسیدگی است"
		n.Link = "/management/wallet?user=" + url.QueryEscape(n.SourceUserID)
		if n.Event == "bank_review" {
			n.Title = "شبای جدید نیازمند بررسی است"
			n.Link += "#banks"
		} else {
			n.Link += "&withdrawal=" + url.QueryEscape(n.SourceID) + "#requests"
		}
		return
	case "ticket", "ticket_operations":
		n.Title = "پاسخ یا وضعیت درخواست پشتیبانی به‌روز شد"
		n.Link = "/support#ticket-" + url.PathEscape(n.SourceID)
		if n.Kind == "ticket_operations" {
			n.Title = "درخواست پشتیبانی جدید ثبت شد"
			base := "/ops"
			if role == "admin" {
				base = "/management/operations"
			}
			n.Link = base + "?ticket=" + url.QueryEscape(n.SourceID) + "#tickets"
		}
		return
	case "editorial":
		n.Title = map[string]string{"updated": "محتوای سایت توسط همکار به‌روز شد", "published": "محتوا منتشر شد", "unpublished": "محتوا به پیش‌نویس برگشت", "layout": "چیدمان مجله به‌روز شد", "review": "بازبینی علمی محتوا ثبت شد"}[n.Event]
		n.Link = "/content/library"
		kind, id, ok := strings.Cut(n.SourceID, ":")
		if kind == "scientific_review" {
			kind = "article"
		}
		if ok && contains([]string{"article", "page", "seo"}, kind) {
			n.Link = "/content/edit/" + kind + "/" + url.PathEscape(id)
		}
		return
	}

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
	if role == "ops" {
		n.Link = "/ops?booking=" + url.QueryEscape(n.BookingID)
	} else if role == "admin" {
		n.Link = "/management/operations?booking=" + url.QueryEscape(n.BookingID)
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
	rows, e := a.DB.Query(r.Context(), "SELECT n.id,n.kind,n.event,COALESCE(n.booking_id,''),COALESCE(n.chat_id,''),n.created_at,n.read_at,COALESCE(n.followup_id,''),n.source_id,COALESCE(n.source_user_id,'') FROM notifications n LEFT JOIN bookings b ON b.id=n.booking_id WHERE n.user_id=$1 AND "+notificationAccess+" ORDER BY n.id DESC LIMIT 100", u.ID, u.Role)
	if e != nil {
		a.fail(w, r, "اعلان‌ها بارگذاری نشد", 500)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var n Notification
		if e = rows.Scan(&n.ID, &n.Kind, &n.Event, &n.BookingID, &n.ChatID, &n.At, &n.ReadAt, &n.FollowupID, &n.SourceID, &n.SourceUserID); e != nil {
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
		_, e = a.DB.Exec(r.Context(), "UPDATE notifications SET read_at=COALESCE(read_at,now()) WHERE id IN (SELECT n.id FROM notifications n LEFT JOIN bookings b ON b.id=n.booking_id WHERE n.user_id=$1 AND n.id<=$3 AND "+notificationAccess+")", u.ID, u.Role, max)
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
	e = a.DB.QueryRow(r.Context(), "UPDATE notifications n SET read_at=COALESCE(n.read_at,now()) WHERE n.id=$3 AND n.user_id=$1 AND EXISTS(SELECT 1 FROM notifications n LEFT JOIN bookings b ON b.id=n.booking_id WHERE n.id=$3 AND n.user_id=$1 AND "+notificationAccess+") RETURNING n.kind,n.event,COALESCE(n.booking_id,''),COALESCE(n.chat_id,''),COALESCE(n.followup_id,''),n.source_id,COALESCE(n.source_user_id,'')", u.ID, u.Role, id).Scan(&n.Kind, &n.Event, &n.BookingID, &n.ChatID, &n.FollowupID, &n.SourceID, &n.SourceUserID)
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
