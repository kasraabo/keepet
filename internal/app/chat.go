package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type ChatMessage struct {
	ID           string    `json:"id"`
	SenderID     string    `json:"-"`
	SenderName   string    `json:"sender_name"`
	Role         string    `json:"role"`
	Body         string    `json:"body"`
	Kind         string    `json:"kind"`
	At           time.Time `json:"at"`
	AttachmentID string    `json:"attachment_id"`
	Mime         string    `json:"mime"`
	Filename     string    `json:"filename"`
	Own          bool      `json:"own"`
	Unread       bool      `json:"unread"`
}
type VetQuestionnaire struct {
	Symptoms, Diagnosis, Prescription, CustomerFeedback string
	CustomerRating                                      int
	At                                                  time.Time
}
type CustomerQuestionnaire struct {
	Rating, Communication, Clarity int
	Body                           string
	At                             time.Time
}
type ChatSummary struct {
	Kind   string
	Unread int

	ID, BookingID, PetName, OtherName, State, Role string
	StartedAt, ExpiresAt, ArchivedAt               *time.Time
	QuestionnaireDone                              bool
}
type ChatView struct {
	BookingStatus string
	Events        []Event

	Kind string

	ID, BookingID, PetName, OwnerName, VetName, Role, State, BackURL string
	StartedAt, ExpiresAt, ArchivedAt                                 *time.Time
	Messages                                                         []ChatMessage
	VetForm                                                          *VetQuestionnaire
	CustomerForm                                                     *CustomerQuestionnaire
	IsParticipant, CanSend, CanEnd, QuestionnaireDone                bool
	DurationMinutes                                                  int
}
type chatRecord struct {
	Kind string

	ID, BookingID, OwnerID, VetID, Status, ProviderID, OwnerName, VetName, PetName, CloseReason string
	StartedAt, ExpiresAt                                                                        time.Time
	ClosedAt                                                                                    *time.Time
	DurationMinutes                                                                             int
}

var errChatNotFound = errors.New("chat not found")

func (a *App) chatDuration(ctx context.Context) int {
	var minutes int
	if a.DB.QueryRow(ctx, "SELECT (data->>'duration_minutes')::integer FROM business_config WHERE kind='chat' AND id='consultation'").Scan(&minutes) != nil || minutes < 5 || minutes > 240 {
		return 30
	}
	return minutes
}
func (a *App) beginChat(ctx context.Context, tx pgx.Tx, b Booking) (string, error) {
	var vet string
	var valid bool
	e := tx.QueryRow(ctx, "SELECT COALESCE(user_id,''),active AND verified AND service='vet' FROM providers WHERE id=$1 FOR SHARE", b.ProviderID).Scan(&vet, &valid)
	if e != nil || !valid || vet == "" || b.OwnerID == "" || b.PaymentStatus != "paid" {
		return "", fmt.Errorf("consultation participants unavailable")
	}
	id := token()
	minutes := 30
	var configured int
	if err := tx.QueryRow(ctx, "SELECT (data->>'duration_minutes')::integer FROM business_config WHERE kind='chat' AND id='consultation'").Scan(&configured); err != nil && err != pgx.ErrNoRows {
		return "", err
	} else if configured >= 5 && configured <= 240 {
		minutes = configured
	}
	e = tx.QueryRow(ctx, `INSERT INTO consultation_chats(id,booking_id,owner_id,vet_id,started_at,expires_at,duration_minutes) SELECT $1,$2,$3,$4,t.at,t.at+make_interval(mins=>$5),$5 FROM (SELECT clock_timestamp() AS at) t ON CONFLICT(booking_id) DO NOTHING RETURNING id`, id, b.ID, b.OwnerID, vet, minutes).Scan(&id)
	if e == pgx.ErrNoRows {
		e = tx.QueryRow(ctx, "SELECT id FROM consultation_chats WHERE booking_id=$1 AND owner_id=$2 AND vet_id=$3 AND closed_at IS NULL AND expires_at>now()", b.ID, b.OwnerID, vet).Scan(&id)
	}
	return id, e
}

// Booking is locked before chat everywhere, so cancellation, expiry, form completion and sends serialize.
func (a *App) lockedChat(ctx context.Context, tx pgx.Tx, id string, u *User) (chatRecord, string, error) {
	var c chatRecord
	var bookingID string
	if u == nil {
		return c, "", errChatNotFound
	}
	e := tx.QueryRow(ctx, "SELECT booking_id FROM consultation_chats WHERE id=$1 AND (owner_id=$2 OR vet_id=$2 OR $3 IN ('admin','ops'))", id, u.ID, u.Role).Scan(&bookingID)
	if e != nil {
		return c, "", errChatNotFound
	}
	e = tx.QueryRow(ctx, "SELECT status,COALESCE(provider_id,''),pet_name FROM bookings WHERE id=$1 FOR UPDATE", bookingID).Scan(&c.Status, &c.ProviderID, &c.PetName)
	if e != nil {
		return c, "", e
	}
	c.BookingID = bookingID
	e = tx.QueryRow(ctx, `SELECT c.id,c.owner_id,c.vet_id,c.started_at,COALESCE(c.expires_at,'9999-12-31 UTC'::timestamptz),c.closed_at,COALESCE(c.duration_minutes,0),c.close_reason,c.kind,o.name,v.name FROM consultation_chats c JOIN users o ON o.id=c.owner_id JOIN users v ON v.id=c.vet_id WHERE c.id=$1 FOR UPDATE OF c`, id).Scan(&c.ID, &c.OwnerID, &c.VetID, &c.StartedAt, &c.ExpiresAt, &c.ClosedAt, &c.DurationMinutes, &c.CloseReason, &c.Kind, &c.OwnerName, &c.VetName)
	if e != nil {
		return c, "", e
	}
	role := "admin"
	if u.ID == c.OwnerID {
		role = "owner"
	} else if u.ID == c.VetID {
		role = "vet"
		if c.Kind != "vet" {
			role = "provider"
		}
	}
	if c.Kind != "vet" {
		var open bool
		if e = tx.QueryRow(ctx, "SELECT chat_is_open(c,b) FROM consultation_chats c JOIN bookings b ON b.id=c.booking_id WHERE c.id=$1", c.ID).Scan(&open); e != nil {
			return c, "", e
		}
		if !open && c.ClosedAt == nil {
			if _, e = tx.Exec(ctx, "UPDATE consultation_chats SET closed_at=now(),close_reason='service_finished' WHERE id=$1", c.ID); e != nil {
				return c, "", e
			}
			c.ClosedAt = ptrTime(time.Now())
		}
		return c, role, nil
	}
	if c.ClosedAt == nil {
		reason := ""
		closeAt := time.Now()
		if !c.ExpiresAt.After(closeAt) {
			reason = "expired"
			closeAt = c.ExpiresAt
		}
		if !contains([]string{"in_progress", "completed"}, c.Status) {
			reason = "booking_unavailable"
		}
		if reason != "" {
			_, e = tx.Exec(ctx, "UPDATE consultation_chats SET closed_at=$2,close_reason=$3 WHERE id=$1 AND closed_at IS NULL", id, closeAt, reason)
			if e != nil {
				return c, "", e
			}
			c.ClosedAt = &closeAt
			c.CloseReason = reason
		}
	}
	if c.ClosedAt != nil && contains([]string{"in_progress", "awaiting_report"}, c.Status) {
		var done bool
		e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM consultation_vet_forms WHERE chat_id=$1)", id).Scan(&done)
		if e != nil {
			return c, "", e
		}
		if done {
			if e = completeConsultation(ctx, tx, c); e != nil {
				return c, "", e
			}
			c.Status = "completed"
		} else if c.Status == "in_progress" {
			var modern bool
			if err := tx.QueryRow(ctx, "SELECT fulfillment_flow FROM bookings WHERE id=$1", c.BookingID).Scan(&modern); err != nil {
				return c, "", err
			}
			if modern {
				if _, err := tx.Exec(ctx, "UPDATE bookings SET status='awaiting_report' WHERE id=$1", c.BookingID); err != nil {
					return c, "", err
				}
				if err := addEvent(ctx, tx, c.BookingID, "", "awaiting_report", "زمان گفتگو پایان یافت؛ پزشک در حال تکمیل گزارش و تجویز است."); err != nil {
					return c, "", err
				}
				c.Status = "awaiting_report"
			}
		}
	}
	return c, role, nil
}
func completeConsultation(ctx context.Context, tx pgx.Tx, c chatRecord) error {
	tag, e := tx.Exec(ctx, `UPDATE bookings SET status='completed',prescription=(SELECT 'علائم: '||symptoms||E'\nتشخیص: '||diagnosis||E'\nتجویز و توصیه: '||prescription FROM consultation_vet_forms WHERE chat_id=$2) WHERE id=$1 AND status IN ('in_progress','awaiting_report') AND EXISTS(SELECT 1 FROM consultation_vet_forms WHERE chat_id=$2)`, c.BookingID, c.ID)
	if e == nil && tag.RowsAffected() > 0 {
		e = addEvent(ctx, tx, c.BookingID, c.VetID, "completed", "ویزیت آنلاین با ثبت علائم، تشخیص و تجویز تکمیل شد؛ گفتگو در پرونده آرشیو شد.")
	}
	return e
}
func (a *App) loadChat(r *http.Request, id string) (*ChatView, error) {
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	c, role, e := a.lockedChat(ctx, tx, id, sess(r).User)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	v := &ChatView{BookingStatus: c.Status, Kind: c.Kind, ID: c.ID, BookingID: c.BookingID, PetName: c.PetName, OwnerName: c.OwnerName, VetName: c.VetName, Role: role, State: "active", StartedAt: &c.StartedAt, ExpiresAt: &c.ExpiresAt, ArchivedAt: c.ClosedAt, DurationMinutes: c.DurationMinutes, IsParticipant: role != "admin", BackURL: "/chats"}
	if c.ClosedAt != nil {
		v.State = "archived"
	}
	v.CanSend = v.IsParticipant && v.State == "active"
	v.CanEnd = role != "owner" && v.State == "active" && c.Kind == "vet"
	if role == "admin" {
		v.BackURL = "/management/chats"
	}
	if c.Kind != "vet" {
		v.ExpiresAt = nil
		v.QuestionnaireDone = true
		rows, err := a.DB.Query(ctx, "SELECT event,created_at FROM booking_events WHERE booking_id=$1 ORDER BY id DESC LIMIT 12", c.BookingID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ev Event
			if err = rows.Scan(&ev.Event, &ev.At); err != nil {
				break
			}
			if status(ev.Event) != ev.Event {
				v.Events = append(v.Events, ev)
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		v.Messages, e = a.chatMessages(ctx, id, sess(r).User.ID)
		return v, e
	}
	// Optional vet opinion is removed at the backend boundary for the customer.
	var f VetQuestionnaire
	e = a.DB.QueryRow(ctx, "SELECT symptoms,diagnosis,prescription,COALESCE(customer_rating,0),customer_feedback,submitted_at FROM consultation_vet_forms WHERE chat_id=$1", id).Scan(&f.Symptoms, &f.Diagnosis, &f.Prescription, &f.CustomerRating, &f.CustomerFeedback, &f.At)
	if e == nil {
		if role == "owner" {
			f.CustomerRating = 0
			f.CustomerFeedback = ""
		}
		v.VetForm = &f
	} else if e != pgx.ErrNoRows {
		return nil, e
	}
	if role != "vet" {
		var f CustomerQuestionnaire
		e = a.DB.QueryRow(ctx, "SELECT rating,communication,clarity,body,submitted_at FROM consultation_customer_forms WHERE chat_id=$1", id).Scan(&f.Rating, &f.Communication, &f.Clarity, &f.Body, &f.At)
		if e == nil {
			v.CustomerForm = &f
		} else if e != pgx.ErrNoRows {
			return nil, e
		}
	}
	if role == "vet" {
		v.QuestionnaireDone = v.VetForm != nil
	} else if role == "owner" {
		v.QuestionnaireDone = v.CustomerForm != nil
	} else {
		v.QuestionnaireDone = true
	}
	v.Messages, e = a.chatMessages(ctx, id, sess(r).User.ID)
	return v, e
}
func (a *App) chatMessages(ctx context.Context, id, user string) ([]ChatMessage, error) {
	rows, e := a.DB.Query(ctx, `SELECT m.id::text,m.sender_id,u.name,CASE WHEN m.sender_id=c.vet_id THEN CASE WHEN c.kind='vet' THEN 'vet' ELSE 'provider' END ELSE 'owner' END,m.body,m.kind,m.created_at,COALESCE(f.id,''),COALESCE(f.mime,''),COALESCE(f.filename,''),($2 IN (c.owner_id,c.vet_id) AND m.sender_id<>$2 AND m.id>COALESCE((SELECT last_message_id FROM chat_reads WHERE chat_id=c.id AND user_id=$2),0)) FROM consultation_messages m JOIN users u ON u.id=m.sender_id JOIN consultation_chats c ON c.id=m.chat_id LEFT JOIN consultation_attachments f ON f.message_id=m.id WHERE m.chat_id=$1 ORDER BY m.id`, id, user)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []ChatMessage{}
	for rows.Next() {
		var m ChatMessage
		if e = rows.Scan(&m.ID, &m.SenderID, &m.SenderName, &m.Role, &m.Body, &m.Kind, &m.At, &m.AttachmentID, &m.Mime, &m.Filename, &m.Unread); e != nil {
			return nil, e
		}
		m.Own = m.SenderID == user
		out = append(out, m)
	}
	return out, rows.Err()
}
func chatJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
func (a *App) chatFailure(w http.ResponseWriter, r *http.Request, msg string, code int) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		chatJSON(w, code, map[string]string{"error": msg})
		return
	}
	a.fail(w, r, msg, code)
}
func (a *App) chatPage(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	v, e := a.loadChat(r, r.PathValue("id"))
	if e != nil {
		a.chatFailure(w, r, "گفتگو پیدا نشد یا دسترسی ندارید", 404)
		return
	}
	p := a.page(r, "گفت‌وگو · "+v.PetName)
	p.Chat = v
	a.chatForms(r, &p)
	a.render(w, r, "chat", p)
}
func (a *App) chatPoll(w http.ResponseWriter, r *http.Request) {
	if sess(r).User == nil {
		chatJSON(w, 401, map[string]string{"error": "برای مشاهدهٔ گفتگو وارد شوید"})
		return
	}
	v, e := a.loadChat(r, r.PathValue("id"))
	if e != nil {
		chatJSON(w, 404, map[string]string{"error": "گفتگو در دسترس نیست"})
		return
	}
	chatJSON(w, 200, map[string]any{"messages": v.Messages, "can_send": v.CanSend, "state": v.State, "expires_at": v.ExpiresAt, "questionnaire_done": v.QuestionnaireDone})
}
func (a *App) chatSend(w http.ResponseWriter, r *http.Request) {
	if sess(r).User == nil {
		a.chatFailure(w, r, "ابتدا وارد شوید", 401)
		return
	}
	id := r.PathValue("id")
	text := strings.TrimSpace(r.FormValue("text"))
	if utf8.RuneCountInString(text) > 4000 {
		a.chatFailure(w, r, "پیام حداکثر ۴۰۰۰ نویسه باشد", 400)
		return
	}
	upload, e := readChatUpload(r)
	if e != nil {
		a.chatFailure(w, r, e.Error(), 400)
		return
	}
	if text == "" && upload == nil {
		a.chatFailure(w, r, "متن یا فایل ارسال کنید", 400)
		return
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.chatFailure(w, r, "ارسال انجام نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	c, role, e := a.lockedChat(ctx, tx, id, sess(r).User)
	if e != nil {
		a.chatFailure(w, r, "گفتگو در دسترس نیست", 404)
		return
	}
	if role == "admin" {
		tx.Commit(ctx)
		a.chatFailure(w, r, "مدیریت فقط امکان مشاهدهٔ گفتگو را دارد", 403)
		return
	}
	if c.ClosedAt != nil {
		tx.Commit(ctx)
		a.chatFailure(w, r, "گفتگو بایگانی شده است؛ پیام‌های قبلی در پرونده محفوظ‌اند", 409)
		return
	}
	if role == "vet" || role == "provider" {
		var active bool
		e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM providers WHERE user_id=$1 AND service=$2 AND active AND verified)", c.VetID, c.Kind).Scan(&active)
		if e != nil || !active {
			a.chatFailure(w, r, "حساب دامپزشک در دسترس نیست", 403)
			return
		}
	}
	var recent int
	e = tx.QueryRow(ctx, "SELECT count(*) FROM consultation_messages WHERE chat_id=$1 AND sender_id=$2 AND created_at>now()-interval '1 minute'", id, sess(r).User.ID).Scan(&recent)
	if e != nil || recent >= 20 {
		a.chatFailure(w, r, "برای ارسال پیام تازه کمی صبر کنید", 429)
		return
	}
	if upload != nil {
		var used int64
		e = tx.QueryRow(ctx, "SELECT COALESCE(sum(octet_length(f.data)),0) FROM consultation_attachments f JOIN consultation_messages m ON m.id=f.message_id WHERE m.chat_id=$1", id).Scan(&used)
		if e != nil || used+int64(len(upload.Data)) > 128<<20 {
			a.chatFailure(w, r, "ظرفیت فایل‌های این گفتگو پر شده است", 400)
			return
		}
	}
	kind := "text"
	if upload != nil {
		kind = upload.Kind
	}
	var msgID int64
	e = tx.QueryRow(ctx, "INSERT INTO consultation_messages(chat_id,sender_id,body,kind,created_at) SELECT $1,$2,$3,$4,clock_timestamp() WHERE EXISTS(SELECT 1 FROM consultation_chats c JOIN bookings b ON b.id=c.booking_id WHERE c.id=$1 AND chat_is_open(c,b) AND (c.kind<>'vet' OR c.expires_at>clock_timestamp())) RETURNING id", id, sess(r).User.ID, text, kind).Scan(&msgID)
	if e == pgx.ErrNoRows {
		// The deadline may pass during validation; never insert a late message.
		if _, _, syncErr := a.lockedChat(ctx, tx, id, sess(r).User); syncErr == nil {
			tx.Commit(ctx)
		}
		a.chatFailure(w, r, "گفتگو بایگانی شده است؛ پیام‌های قبلی در پرونده محفوظ‌اند", 409)
		return
	}
	if e == nil && upload != nil {
		_, e = tx.Exec(ctx, "INSERT INTO consultation_attachments(id,message_id,mime,filename,data) VALUES($1,$2,$3,$4,$5)", token(), msgID, upload.Mime, upload.Filename, upload.Data)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.chatFailure(w, r, "پیام ذخیره نشد؛ دوباره تلاش کنید", 500)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		chatJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	redirect(w, r, "/chats/"+id)
}
func (a *App) chatEnd(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	c, role, e := a.lockedChat(ctx, tx, r.PathValue("id"), sess(r).User)
	if e != nil {
		a.fail(w, r, "گفتگو در دسترس نیست", 404)
		return
	}
	if c.Kind != "vet" {
		a.fail(w, r, "این کنترل مخصوص ویزیت پزشک است", 409)
		return
	}
	if role == "owner" {
		a.fail(w, r, "پایان ویزیت با دامپزشک یا مدیریت است؛ برای خروج، فرم خود را تکمیل کنید", 403)
		return
	}
	if c.ClosedAt == nil {
		_, e = tx.Exec(ctx, "UPDATE consultation_chats SET closed_at=now(),close_reason='ended' WHERE id=$1", c.ID)
		c.ClosedAt = ptrTime(time.Now())
		if e == nil {
			e = completeConsultation(ctx, tx, c)
		}
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "پایان گفتگو ثبت نشد", 500)
		return
	}
	to := "/chats/" + c.ID + "/questionnaire"
	if role == "admin" {
		to = "/management/chats/" + c.ID
	}
	redirect(w, r, to)
}
func ptrTime(t time.Time) *time.Time { return &t }
func (a *App) chatQuestionnairePage(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	v, e := a.loadChat(r, r.PathValue("id"))
	if e != nil {
		a.fail(w, r, "گفتگو در دسترس نیست", 404)
		return
	}
	if !v.IsParticipant {
		a.fail(w, r, "این فرم مخصوص شرکت‌کنندگان گفتگو است", 403)
		return
	}
	if v.Kind != "vet" {
		a.fail(w, r, "پرسشنامهٔ پزشکی برای این خدمت وجود ندارد", 409)
		return
	}
	p := a.page(r, "فرم پایان ویزیت")
	p.Chat = v
	a.chatForms(r, &p)
	a.render(w, r, "chat-questionnaire", p)
}
func (a *App) chatQuestionnaire(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	c, role, e := a.lockedChat(ctx, tx, r.PathValue("id"), sess(r).User)
	if e != nil {
		a.fail(w, r, "گفتگو در دسترس نیست", 404)
		return
	}
	if c.Kind != "vet" {
		a.fail(w, r, "پرسشنامهٔ پزشکی برای این خدمت وجود ندارد", 409)
		return
	}
	if role == "admin" {
		a.fail(w, r, "مدیریت نمی‌تواند فرم شرکت‌کنندگان را ثبت کند", 403)
		return
	}
	scope := "customer-survey"
	if role == "vet" {
		scope = "vet-report"
	}
	f := a.form(ctx, scope, nil)
	answers, validationErr := formAnswers(r, f)
	if validationErr != nil {
		tx.Rollback(ctx)
		a.chatQuestionnaireError(w, r, validationErr.Error())
		return
	}
	var previousResponse bool
	if role != "vet" {
		tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM consultation_customer_forms WHERE chat_id=$1)", c.ID).Scan(&previousResponse)
	}
	if role == "vet" {
		symptoms, diagnosis, prescription, feedback := strings.TrimSpace(r.FormValue("symptoms")), strings.TrimSpace(r.FormValue("diagnosis")), strings.TrimSpace(r.FormValue("prescription")), strings.TrimSpace(r.FormValue("customer_feedback"))
		rating, _ := strconv.Atoi(normalize(r.FormValue("customer_rating")))
		if symptoms == "" || diagnosis == "" || prescription == "" || utf8.RuneCountInString(symptoms) > 6000 || utf8.RuneCountInString(diagnosis) > 6000 || utf8.RuneCountInString(prescription) > 6000 || utf8.RuneCountInString(feedback) > 3000 || (r.FormValue("customer_rating") != "" && r.FormValue("customer_rating") != "0" && (rating < 1 || rating > 5)) {
			tx.Rollback(ctx)
			a.chatQuestionnaireError(w, r, "علائم، تشخیص و تجویز لازم است؛ امتیاز مشتری اختیاری و بین ۱ تا ۵ باشد")
			return
		}
		var previous *time.Time
		tx.QueryRow(ctx, "SELECT submitted_at FROM consultation_vet_forms WHERE chat_id=$1", c.ID).Scan(&previous)
		if previous != nil && c.ClosedAt != nil {
			tx.Commit(ctx)
			redirect(w, r, "/chats/"+c.ID)
			return
		}
		_, e = tx.Exec(ctx, `INSERT INTO consultation_vet_forms(chat_id,symptoms,diagnosis,prescription,customer_rating,customer_feedback) VALUES($1,$2,$3,$4,NULLIF($5,0),$6) ON CONFLICT(chat_id) DO UPDATE SET symptoms=excluded.symptoms,diagnosis=excluded.diagnosis,prescription=excluded.prescription,customer_rating=excluded.customer_rating,customer_feedback=excluded.customer_feedback,submitted_at=now()`, c.ID, symptoms, diagnosis, prescription, rating, feedback)
		if e == nil && c.ClosedAt != nil {
			e = completeConsultation(ctx, tx, c)
		}
	} else {
		rating, _ := strconv.Atoi(normalize(r.FormValue("rating")))
		communication, _ := strconv.Atoi(normalize(r.FormValue("communication")))
		clarity, _ := strconv.Atoi(normalize(r.FormValue("clarity")))
		body := strings.TrimSpace(r.FormValue("body"))
		if body == "" {
			body = strings.TrimSpace(r.FormValue("comment"))
		}
		if rating < 1 || rating > 5 || communication < 1 || communication > 5 || clarity < 1 || clarity > 5 || utf8.RuneCountInString(body) > 3000 {
			tx.Rollback(ctx)
			a.chatQuestionnaireError(w, r, "هر سه امتیاز بین ۱ تا ۵ لازم است؛ متن حداکثر ۳۰۰۰ نویسه باشد")
			return
		}
		_, e = tx.Exec(ctx, `INSERT INTO consultation_customer_forms(chat_id,rating,communication,clarity,body) VALUES($1,$2,$3,$4,$5) ON CONFLICT(chat_id) DO NOTHING`, c.ID, rating, communication, clarity, body)
	}
	if e == nil && !previousResponse {
		e = saveFormResponse(ctx, tx, f, c.ID, sess(r).User.ID, answers)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		tx.Rollback(ctx)
		a.chatQuestionnaireErrorStatus(w, r, "فرم ذخیره نشد؛ اطلاعات واردشده حفظ شده است", 500)
		return
	}
	redirect(w, r, noticeURL("/chats/"+c.ID, "فرم شما در پرونده ذخیره شد."))
}

func (a *App) chatQuestionnaireError(w http.ResponseWriter, r *http.Request, msg string) {
	a.chatQuestionnaireErrorStatus(w, r, msg, 400)
}
func (a *App) chatQuestionnaireErrorStatus(w http.ResponseWriter, r *http.Request, msg string, code int) {
	v, err := a.loadChat(r, r.PathValue("id"))
	if err != nil || !v.IsParticipant {
		a.fail(w, r, msg, code)
		return
	}
	if !v.QuestionnaireDone || v.Role == "vet" && v.State == "active" {
		if v.Role == "vet" {
			rating, _ := strconv.Atoi(normalize(r.FormValue("customer_rating")))
			v.VetForm = &VetQuestionnaire{Symptoms: r.FormValue("symptoms"), Diagnosis: r.FormValue("diagnosis"), Prescription: r.FormValue("prescription"), CustomerFeedback: r.FormValue("customer_feedback"), CustomerRating: rating}
		} else {
			rating, _ := strconv.Atoi(normalize(r.FormValue("rating")))
			communication, _ := strconv.Atoi(normalize(r.FormValue("communication")))
			clarity, _ := strconv.Atoi(normalize(r.FormValue("clarity")))
			v.CustomerForm = &CustomerQuestionnaire{Rating: rating, Communication: communication, Clarity: clarity, Body: r.FormValue("body")}
		}
	}
	p := a.page(r, "فرم پایان ویزیت")
	p.Chat = v
	a.chatForms(r, &p)
	p.Error = msg
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	a.render(w, r, "chat-questionnaire", p)
}
func (a *App) chatMedia(w http.ResponseWriter, r *http.Request) {
	if sess(r).User == nil {
		http.Error(w, "ورود لازم است", 401)
		return
	}
	u := sess(r).User
	var data []byte
	var mimeType, filename string
	var at time.Time
	e := a.DB.QueryRow(r.Context(), `SELECT f.data,f.mime,f.filename,f.created_at FROM consultation_attachments f JOIN consultation_messages m ON m.id=f.message_id JOIN consultation_chats c ON c.id=m.chat_id WHERE f.id=$1 AND (c.owner_id=$2 OR c.vet_id=$2 OR $3 IN ('admin','ops'))`, r.PathValue("id"), u.ID, u.Role).Scan(&data, &mimeType, &filename, &at)
	if e != nil {
		http.Error(w, "فایل در دسترس نیست", 404)
		return
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": filename}))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	http.ServeContent(w, r, filename, at, bytes.NewReader(data))
}
func (a *App) chatSummaries(r *http.Request, booking string) ([]ChatSummary, error) {
	u := sess(r).User
	if u == nil {
		return nil, nil
	}
	rows, e := a.DB.Query(r.Context(), `SELECT c.id,c.booking_id,b.pet_name,CASE WHEN c.owner_id=$1 THEN v.name ELSE o.name END,CASE WHEN chat_is_open(c,b) THEN 'active' ELSE 'archived' END,CASE WHEN c.owner_id=$1 THEN 'owner' WHEN c.vet_id=$1 THEN CASE WHEN c.kind='vet' THEN 'vet' ELSE 'provider' END ELSE 'admin' END,c.started_at,c.expires_at,CASE WHEN c.closed_at IS NOT NULL THEN c.closed_at WHEN c.expires_at<=now() THEN c.expires_at ELSE NULL END,CASE WHEN c.kind<>'vet' THEN true WHEN c.owner_id=$1 THEN EXISTS(SELECT 1 FROM consultation_customer_forms f WHERE f.chat_id=c.id) WHEN c.vet_id=$1 THEN EXISTS(SELECT 1 FROM consultation_vet_forms f WHERE f.chat_id=c.id) ELSE true END,c.kind,CASE WHEN $1 IN (c.owner_id,c.vet_id) THEN (SELECT count(*) FROM consultation_messages m WHERE m.chat_id=c.id AND m.sender_id<>$1 AND m.id>COALESCE((SELECT last_message_id FROM chat_reads WHERE chat_id=c.id AND user_id=$1),0)) ELSE 0 END FROM consultation_chats c JOIN bookings b ON b.id=c.booking_id JOIN users o ON o.id=c.owner_id JOIN users v ON v.id=c.vet_id WHERE (c.owner_id=$1 OR c.vet_id=$1 OR $2 IN ('admin','ops')) AND ($3='' OR c.booking_id=$3) ORDER BY c.started_at DESC LIMIT 200`, u.ID, u.Role, booking)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []ChatSummary{}
	for rows.Next() {
		var c ChatSummary
		if e = rows.Scan(&c.ID, &c.BookingID, &c.PetName, &c.OtherName, &c.State, &c.Role, &c.StartedAt, &c.ExpiresAt, &c.ArchivedAt, &c.QuestionnaireDone, &c.Kind, &c.Unread); e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (a *App) chatArchive(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/management/") && !a.auth(w, r, "admin", "ops") {
		return
	}
	p := a.page(r, "گفتگوها و پروندهٔ ویزیت")
	var e error
	p.Chats, e = a.chatSummaries(r, "")
	if e != nil {
		a.fail(w, r, "پرونده‌ها بارگذاری نشد", 500)
		return
	}
	a.render(w, r, "chat-archives", p)
}
func (a *App) changeChatDuration(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin", "ops") {
		return
	}
	minutes, e := strconv.Atoi(normalize(r.FormValue("duration_minutes")))
	if e != nil || minutes < 5 || minutes > 240 {
		a.fail(w, r, "مدت بین ۵ تا ۲۴۰ دقیقه باشد", 400)
		return
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	c, _, e := a.lockedChat(ctx, tx, r.PathValue("id"), sess(r).User)
	if e != nil {
		a.fail(w, r, "گفتگو در دسترس نیست", 404)
		return
	}
	if c.Kind != "vet" {
		a.fail(w, r, "مدت ویزیت برای چت هماهنگی تنظیم نمی‌شود", 409)
		return
	}
	if c.ClosedAt != nil {
		tx.Commit(ctx)
		a.fail(w, r, "گفتگوی آرشیوشده دوباره باز نمی‌شود", 409)
		return
	}
	if c.ExpiresAt.Format(time.RFC3339) != r.FormValue("expected_expires") {
		a.fail(w, r, "زمان گفتگو تغییر کرده؛ صفحه را تازه کنید", 409)
		return
	}
	expiry := c.StartedAt.Add(time.Duration(minutes) * time.Minute)
	if !expiry.After(time.Now()) {
		a.fail(w, r, "مدت تازه نباید گفتگو را به گذشته منقضی کند", 400)
		return
	}
	_, e = tx.Exec(ctx, "UPDATE consultation_chats SET expires_at=$2,duration_minutes=$3 WHERE id=$1", c.ID, expiry, minutes)
	if e == nil {
		before, _ := json.Marshal(map[string]any{"duration_minutes": c.DurationMinutes, "expires_at": c.ExpiresAt})
		after, _ := json.Marshal(map[string]any{"duration_minutes": minutes, "expires_at": expiry})
		_, e = tx.Exec(ctx, "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,'chat_duration',$2,$3,$4)", sess(r).User.ID, c.ID, before, after)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "زمان ذخیره نشد", 500)
		return
	}
	redirect(w, r, "/management/chats/"+c.ID)
}
