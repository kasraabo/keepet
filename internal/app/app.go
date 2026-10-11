package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/a-h/templ"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"html/template"
	"io/fs"
	assets "keepet"
	"keepet/internal/ui"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	_ "time/tzdata"
)

type App struct {
	DB        *pgxpool.Pool
	Templates *template.Template
	Location  *time.Location
	Triage    []Question
}

func New(ctx context.Context) (*App, error) {
	if os.Getenv("APP_MODE") != "development" {
		return nil, errors.New("APP_MODE=development is required: real OTP/payment adapters are not configured; production is deliberately disabled")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err = db.Ping(ctx); err != nil {
		db.Close()
		return nil, err
	}

	source, err := iofs.New(assets.Files, "migrations")
	if err != nil {
		db.Close()
		return nil, err
	}
	migration, err := migrate.NewWithSourceInstance("iofs", source, dsn)
	if err != nil {
		source.Close()
		db.Close()
		return nil, err
	}
	err = migration.Up()
	sourceErr, dbErr := migration.Close()
	if err != nil && err != migrate.ErrNoChange {
		db.Close()
		return nil, err
	}
	if sourceErr != nil {
		db.Close()
		return nil, sourceErr
	}
	if dbErr != nil {
		db.Close()
		return nil, dbErr
	}
	loc, err := time.LoadLocation("Asia/Tehran")
	if err != nil {
		db.Close()
		return nil, err
	}

	t, err := template.New("pages").Funcs(template.FuncMap{"money": money, "providerOriginal": func(p Provider) string {
		b, _ := json.Marshal(map[string]any{"active": p.Active, "price": p.Price, "areas": p.Areas})
		return string(b)
	}, "digits": digits, "status": status, "service": service, "area": areaName, "period": period, "has": contains, "add": func(a, b int) int { return a + b }, "iso": func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.Format(time.RFC3339)
	}, "stamp": func(t time.Time) string { return t.Format(time.RFC3339) }, "short": func(s string) string {
		if len(s) > 8 {
			return strings.ToUpper(s[:8])
		}
		return s
	}, "eqs": func(a, b string) bool { return a == b }}).ParseFS(assets.Files, "web/templates/*.html")
	if err != nil {
		db.Close()
		return nil, err
	}
	triageData, _ := assets.Files.ReadFile("config/triage.json")
	if path := os.Getenv("TRIAGE_CONFIG"); path != "" {
		triageData, err = os.ReadFile(path)
		if err != nil {
			db.Close()
			return nil, err
		}
	}
	var qs []Question
	if err = json.Unmarshal(triageData, &qs); err != nil {
		db.Close()
		return nil, err
	}
	a := &App{DB: db, Templates: t, Location: loc, Triage: qs}
	if err = a.seed(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return a, nil
}
func (a *App) Close() { a.DB.Close() }
func token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func hash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func (a *App) seed(ctx context.Context) error {
	type demo struct{ phone, name, role, id string }
	users := []demo{{"09120000001", "کارشناس عملیات", "ops", "demo-ops"}, {"09120000002", "دکتر نازنین مهر", "provider", "demo-vet"}, {"09120000003", "سارا رضایی", "provider", "demo-groomer"}, {"09120000004", "امین احمدی", "provider", "demo-sampler"}, {"09120000005", "مدیر کیپت", "admin", "demo-admin"}, {"09120000006", "دکتر آرمان نوری", "provider", "demo-vet2"}}
	for _, u := range users {
		if _, err := a.DB.Exec(ctx, "INSERT INTO users(id,phone,name,role) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", u.id, u.phone, u.name, u.role); err != nil {
			return err
		}
	}
	ps := []Provider{{ID: "vet-nazanin", UserID: "demo-vet", Name: "دکتر نازنین مهر", Service: "vet", Bio: "مشاورهٔ سلامت سگ و گربه، تغذیه و مراقبت‌های پیشگیرانه", License: "DEMO-1001", Avatar: "vet", Price: 350000, Experience: 8}, {ID: "vet-arman", UserID: "demo-vet2", Name: "دکتر آرمان نوری", Service: "vet", Bio: "دامپزشکی حیوانات کوچک و مشاورهٔ بیماری‌های داخلی", License: "DEMO-1002", Avatar: "vet2", Price: 400000, Experience: 6}, {ID: "groom-sara", UserID: "demo-groomer", Name: "سارا رضایی", Service: "grooming", Bio: "پیرایش آرام و بدون عجله، با تجربهٔ مراقبت از گربه‌ها", Avatar: "groomer", Price: 0, Experience: 5}, {ID: "sample-amin", UserID: "demo-sampler", Name: "امین احمدی", Service: "sampling", Bio: "نمونه‌برداری ساده زیر نظر دامپزشک و انتقال به آزمایشگاه", Avatar: "sampler", Price: 0, Experience: 4}}
	for _, p := range ps {
		_, err := a.DB.Exec(ctx, "INSERT INTO providers(id,user_id,name,service,bio,license,verified,active,price,avatar,experience) VALUES($1,$2,$3,$4,$5,$6,true,true,$7,$8,$9) ON CONFLICT DO NOTHING", p.ID, p.UserID, p.Name, p.Service, p.Bio, p.License, p.Price, p.Avatar, p.Experience)
		if err != nil {
			return err
		}
		for d := 0; d < 7; d++ {
			day := time.Now().In(a.Location).AddDate(0, 0, d).Format("2006-01-02")
			for _, period := range []string{"morning", "afternoon", "night"} {
				id := p.ID + "-" + day + "-" + period
				_, err = a.DB.Exec(ctx, "INSERT INTO availability(id,provider_id,day,period,capacity) VALUES($1,$2,$3,$4,2) ON CONFLICT DO NOTHING", id, p.ID, day, period)
				if err != nil {
					return err
				}
			}
		}
	}
	return a.seedBusiness(ctx)
}

type key int

const sessionKey key = 0

func sess(r *http.Request) *Session { return r.Context().Value(sessionKey).(*Session) }
func (a *App) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/static/") || r.URL.Path == "/sw.js" || r.URL.Path == "/manifest.webmanifest" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		s := &Session{}
		c, err := r.Cookie("keepet_session")
		if err == nil {
			err = a.DB.QueryRow(r.Context(), "SELECT token,csrf FROM sessions WHERE token=$1 AND expires_at>now()", c.Value).Scan(&s.Token, &s.CSRF)
		}
		if err != nil || s.Token == "" {
			s.Token = token()
			s.CSRF = token()
			if _, err = a.DB.Exec(r.Context(), "INSERT INTO sessions(token,csrf,expires_at) VALUES($1,$2,$3)", s.Token, s.CSRF, time.Now().Add(7*24*time.Hour)); err != nil {
				http.Error(w, "دیتابیس در دسترس نیست", 503)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "keepet_session", Value: s.Token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: 604800})
		}
		u := &User{}
		if a.DB.QueryRow(r.Context(), "SELECT u.id,u.phone,u.name,u.role FROM users u JOIN sessions s ON s.user_id=u.id WHERE s.token=$1", s.Token).Scan(&u.ID, &u.Phone, &u.Name, &u.Role) == nil {
			s.User = u
		}
		r = r.WithContext(context.WithValue(r.Context(), sessionKey, s))
		if r.Method == "POST" {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			if r.ParseForm() != nil {
				http.Error(w, "فرم نامعتبر است", 400)
				return
			}
			if r.FormValue("csrf") != s.CSRF {
				http.Error(w, "نشست فرم منقضی شده؛ صفحه را تازه کنید", 403)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (a *App) Handler() http.Handler {
	m := http.NewServeMux()
	static, _ := fs.Sub(assets.Files, "web/static")
	m.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	for _, p := range []string{"sw.js", "manifest.webmanifest"} {
		m.HandleFunc("GET /"+p, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/sw.js" {
				w.Header().Set("Service-Worker-Allowed", "/")
				w.Header().Set("Content-Type", "application/javascript")
			} else {
				w.Header().Set("Content-Type", "application/manifest+json")
			}
			b, _ := assets.Files.ReadFile("web/static" + r.URL.Path)
			w.Write(b)
		})
	}
	m.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if a.DB.Ping(r.Context()) != nil {
			http.Error(w, "database unavailable", 503)
			return
		}
		fmt.Fprint(w, "ok")
	})
	m.HandleFunc("GET /{$}", a.home)
	m.HandleFunc("GET /services/{service}", a.landing)
	m.HandleFunc("POST /book/{service}", a.createBooking)
	m.HandleFunc("GET /booking/{id}", a.bookingPage)
	m.HandleFunc("POST /booking/{id}/step", a.bookingStep)
	m.HandleFunc("POST /booking/{id}/pay", a.pay)
	m.HandleFunc("GET /login", a.loginPage)
	m.HandleFunc("POST /auth/request", a.requestOTP)
	m.HandleFunc("POST /auth/verify", a.verifyOTP)
	m.HandleFunc("POST /logout", a.logout)
	m.HandleFunc("GET /dashboard", a.dashboard)
	m.HandleFunc("GET /bookings/{id}", a.detail)
	m.HandleFunc("POST /bookings/{id}/cancel", a.cancel)
	m.HandleFunc("POST /bookings/{id}/replacement", a.replacement)
	m.HandleFunc("POST /bookings/{id}/review", a.review)
	m.HandleFunc("POST /pets", a.addPet)
	m.HandleFunc("GET /ops", a.ops)
	m.HandleFunc("POST /ops/bookings/{id}", a.opsAction)
	m.HandleFunc("POST /ops/availability", a.addAvailability)
	m.HandleFunc("POST /ops/applications/{id}", a.applicationDecision)
	m.HandleFunc("POST /ops/tickets/{id}", a.replyTicket)
	m.HandleFunc("GET /provider", a.providerPanel)
	m.HandleFunc("POST /provider/bookings/{id}", a.providerAction)
	m.HandleFunc("POST /provider/availability", a.providerAvailability)
	m.HandleFunc("GET /join", a.join)
	m.HandleFunc("POST /join", a.apply)
	m.HandleFunc("GET /support", a.support)
	m.HandleFunc("POST /support", a.ticket)
	m.HandleFunc("GET /admin", a.admin)
	m.HandleFunc("GET /admin/{section}", a.admin)
	m.HandleFunc("POST /admin/{kind}/{id}", a.businessSave)
	m.HandleFunc("POST /admin/payout/{id}", a.businessPayout)
	m.HandleFunc("GET /admin/export.csv", a.businessExport)
	m.HandleFunc("GET /magazine/{slug}", a.article)
	m.HandleFunc("POST /provider/articles/{id}/review", a.reviewArticle)
	m.HandleFunc("GET /pages/{slug}", a.publicPage)
	return a.middleware(m)
}
func (a *App) page(r *http.Request, title string) Page {
	s := sess(r)
	p := Page{Questions: a.questions(r.Context()), Title: title, Path: r.URL.Path, CSRF: s.CSRF, User: s.User, Services: Services, Areas: Areas, Menu: GroomMenu, Samples: Samples, Notice: r.URL.Query().Get("notice")}
	a.livePage(r.Context(), &p)
	return p
}
func (a *App) render(w http.ResponseWriter, r *http.Request, name string, p Page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if p.User != nil || strings.HasPrefix(r.URL.Path, "/booking") || r.URL.Path == "/login" {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	}
	var b strings.Builder
	if err := a.Templates.ExecuteTemplate(&b, name, p); err != nil {
		log.Printf("render: %v", err)
		http.Error(w, "خطای نمایش صفحه", 500)
		return
	}
	component := templ.Raw(b.String())
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Retarget", "body")
		w.Header().Set("HX-Reswap", "innerHTML")
		component.Render(r.Context(), w)
		return
	}
	ui.Document(p.Title, component).Render(r.Context(), w)
}
func redirect(w http.ResponseWriter, r *http.Request, to string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(200)
	} else {
		http.Redirect(w, r, to, 303)
	}
}
func (a *App) fail(w http.ResponseWriter, r *http.Request, msg string, code int) {
	p := a.page(r, "خطا")
	p.Error = msg
	w.WriteHeader(code)
	a.render(w, r, "error", p)
}
func (a *App) auth(w http.ResponseWriter, r *http.Request, roles ...string) bool {
	u := sess(r).User
	if u == nil {
		redirect(w, r, "/login?next="+r.URL.Path)
		return false
	}
	if len(roles) > 0 && !contains(roles, u.Role) {
		a.fail(w, r, "به این بخش دسترسی ندارید.", 403)
		return false
	}
	return true
}
func (a *App) home(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "home", a.page(r, "مراقبت از پت، با خیال راحت"))
}
func (a *App) landing(w http.ResponseWriter, r *http.Request) {
	s := service(r.PathValue("service"))
	if s.ID == "" || !a.enabled(r.Context(), "service", s.ID) {
		a.fail(w, r, "خدمت پیدا نشد.", 404)
		return
	}
	p := a.page(r, s.Name)
	for _, live := range p.Services {
		if live.ID == s.ID {
			s = live
		}
	}
	p.Service = s
	p.Providers, _ = a.providers(r.Context(), s.ID)
	a.render(w, r, "landing", p)
}
func (a *App) providers(ctx context.Context, svc string) ([]Provider, error) {
	rows, err := a.DB.Query(ctx, "SELECT id,COALESCE(user_id,''),name,service,bio,license,avatar,verified,active,species,areas,price,experience FROM providers WHERE active AND ($1='' OR service=$1) ORDER BY id", svc)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ps []Provider
	for rows.Next() {
		var p Provider
		if err = rows.Scan(&p.ID, &p.UserID, &p.Name, &p.Service, &p.Bio, &p.License, &p.Avatar, &p.Verified, &p.Active, &p.Species, &p.Areas, &p.Price, &p.Experience); err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	return ps, rows.Err()
}

const bookingColumns = "id,session_token,COALESCE(owner_id,''),service,stage,pet_name,species,age,weight,answers,area,address,COALESCE(provider_id,''),COALESCE(availability_id,''),items,sample,consent,amount,status,payment_status,payment_key,created_at,paid_at,exact_time,meeting_url,prescription,result,COALESCE(replacement_id,''),refund_amount,refund_reason,refund_status,notes"

func scanBooking(row pgx.Row) (Booking, error) {
	var b Booking
	err := row.Scan(&b.ID, &b.SessionToken, &b.OwnerID, &b.Service, &b.Stage, &b.PetName, &b.Species, &b.Age, &b.Weight, &b.Answers, &b.Area, &b.Address, &b.ProviderID, &b.AvailabilityID, &b.Items, &b.Sample, &b.Consent, &b.Amount, &b.Status, &b.PaymentStatus, &b.PaymentKey, &b.CreatedAt, &b.PaidAt, &b.ExactTime, &b.MeetingURL, &b.Prescription, &b.Result, &b.ReplacementID, &b.RefundAmount, &b.RefundReason, &b.RefundStatus, &b.Notes)
	return b, err
}
func (a *App) getBooking(r *http.Request) (Booking, error) {
	return scanBooking(a.DB.QueryRow(r.Context(), "SELECT "+bookingColumns+" FROM bookings WHERE id=$1", r.PathValue("id")))
}
func owned(r *http.Request, b Booking) bool {
	s := sess(r)
	return s.User != nil && b.OwnerID == s.User.ID || b.Status == "draft" && b.SessionToken == s.Token
}
func (a *App) bookings(ctx context.Context, where string, args ...any) ([]Booking, error) {
	rows, err := a.DB.Query(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE "+where+" ORDER BY paid_at DESC NULLS LAST,created_at DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bs []Booking
	for rows.Next() {
		b, err := scanBooking(rows)
		if err != nil {
			return nil, err
		}
		bs = append(bs, b)
	}
	return bs, rows.Err()
}
func addEvent(ctx context.Context, tx pgx.Tx, bid, actor, event, detail string) error {
	_, err := tx.Exec(ctx, "INSERT INTO booking_events(booking_id,actor_id,event,detail) VALUES($1,NULLIF($2,''),$3,$4)", bid, actor, event, detail)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"event": event, "detail": detail})
	_, err = tx.Exec(ctx, "INSERT INTO outbox(booking_id,kind,payload,dedup_key) VALUES($1,$2,$3,$4)", bid, event, payload, token())
	return err
}
