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
	GoogleHTTP *http.Client
	DB         *pgxpool.Pool
	Templates  *template.Template
	Location   *time.Location
	Triage     []Question
	chatCancel context.CancelFunc
	chatDone   chan struct{}
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

	t, err := template.New("pages").Funcs(template.FuncMap{"requestPanel": func(p Page, b Booking) Page { p.Booking = &b; return p }, "editorialVideo": editorialVideo, "editorialPosition": editorialPosition, "referralPercent": referralPercent, "referralKind": referralKind, "optionEnabled": optionEnabled, "walletStatus": walletStatus, "money": money, "jalali": jalaliDate, "bookingAnswers": bookingAnswers, "json": func(v any) string { return string(mustJSON(v)) }, "providerOriginal": func(p Provider) string {
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
	a.startChatMaintenance()
	return a, nil
}
func (a *App) Close() {
	if a.chatCancel != nil {
		a.chatCancel()
		<-a.chatDone
	}
	a.DB.Close()
}
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
		var sampleRate any
		if p.Service == "sampling" {
			sampleRate = int64(200000)
		} // New development account only; never replace an existing provider tariff.
		_, err := a.DB.Exec(ctx, "INSERT INTO providers(id,user_id,name,service,bio,license,verified,active,price,avatar,experience,sampling_rate) VALUES($1,$2,$3,$4,$5,$6,true,true,$7,$8,$9,$10) ON CONFLICT DO NOTHING", p.ID, p.UserID, p.Name, p.Service, p.Bio, p.License, p.Price, p.Avatar, p.Experience, sampleRate)
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
	if e := a.seedBusiness(ctx); e != nil {
		return e
	}
	return a.seedLabs(ctx)
}

type key int

const sessionKey key = 0

func sess(r *http.Request) *Session { return r.Context().Value(sessionKey).(*Session) }
func (a *App) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self' blob:; connect-src 'self'; frame-src https://www.youtube-nocookie.com https://www.aparat.com; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
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
			http.SetCookie(w, &http.Cookie{Name: "keepet_session", Value: s.Token, Path: "/", HttpOnly: true, Secure: secureSessionCookie(r), SameSite: http.SameSiteLaxMode, MaxAge: 604800})
		}
		u := &User{}
		if a.DB.QueryRow(r.Context(), "SELECT u.id,u.phone,u.name,u.role FROM users u JOIN sessions s ON s.user_id=u.id WHERE s.token=$1", s.Token).Scan(&u.ID, &u.Phone, &u.Name, &u.Role) == nil {
			s.User = u
		}
		r = r.WithContext(context.WithValue(r.Context(), sessionKey, s))
		if r.Method == "POST" {
			var parseErr error
			if (strings.HasPrefix(r.URL.Path, "/chats/") && strings.HasSuffix(r.URL.Path, "/messages") || r.URL.Path == "/provider/media" || r.URL.Path == "/content/media" || (strings.HasPrefix(r.URL.Path, "/applications/") && strings.HasSuffix(r.URL.Path, "/documents")) || strings.HasPrefix(r.URL.Path, "/management/wallet/withdrawals/") || (strings.HasPrefix(r.URL.Path, "/bookings/") && (strings.HasSuffix(r.URL.Path, "/fulfillment") || strings.HasSuffix(r.URL.Path, "/laboratory")))) && strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				// Keep ordinary requests short, while allowing bounded mobile uploads.
				controller := http.NewResponseController(w)
				_ = controller.SetReadDeadline(time.Now().Add(90 * time.Second))
				_ = controller.SetWriteDeadline(time.Now().Add(120 * time.Second))
				uploadLimit := int64(14 << 20)
				if r.URL.Path == "/content/media" {
					uploadLimit = 66 << 20
				}
				r.Body = http.MaxBytesReader(w, r.Body, uploadLimit)
				parseErr = r.ParseMultipartForm(1 << 20)
				if r.MultipartForm != nil {
					defer r.MultipartForm.RemoveAll()
				}
			} else {
				r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
				parseErr = r.ParseForm()
			}
			if parseErr != nil {
				code := 400
				var tooLarge *http.MaxBytesError
				if errors.As(parseErr, &tooLarge) {
					code = 413
				}
				a.chatFailure(w, r, "فرم نامعتبر است یا حجم فایل بیش از حد مجاز است", code)
				return
			}
			if r.FormValue("csrf") != s.CSRF {
				a.chatFailure(w, r, "نشست فرم منقضی شده؛ صفحه را تازه کنید", 403)
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

	m.HandleFunc("GET /referrals", a.referralsPage)
	m.HandleFunc("GET /management/referrals", a.referralsPage)
	m.HandleFunc("POST /management/referrals/settings", a.saveReferralSettings)
	m.HandleFunc("POST /management/referrals/{kind}/{id}", a.toggleReferral)
	m.HandleFunc("POST /booking/{id}/request", a.submitRequest)
	m.HandleFunc("POST /bookings/{id}/request-action", a.requestAction)
	m.HandleFunc("GET /management/booking-policy", a.approvalSettingsPage)
	m.HandleFunc("POST /management/booking-policy", a.saveApprovalSettings)
	m.HandleFunc("POST /management/booking-policy/providers/{id}", a.saveProviderApproval)
	m.HandleFunc("GET /bookings/{id}/fulfillment", a.fulfillmentPage)
	m.HandleFunc("POST /bookings/{id}/fulfillment", a.fulfillmentAction)
	m.HandleFunc("GET /bookings/{id}/handover/{receipt}", a.handoverDownload)
	m.HandleFunc("GET /chats/{id}/follow-up", a.followupForm)
	m.HandleFunc("POST /chats/{id}/follow-up", a.proposeFollowup)
	m.HandleFunc("GET /follow-ups", a.followupList)
	m.HandleFunc("GET /follow-ups/{id}", a.followupPage)
	m.HandleFunc("POST /follow-ups/{id}/action", a.followupAction)
	m.HandleFunc("GET /management/follow-ups", a.followupList)
	m.HandleFunc("GET /notifications", a.notificationsPage)
	m.HandleFunc("GET /notifications/count", a.notificationsCount)
	m.HandleFunc("POST /notifications/{id}/read", a.notificationRead)
	m.HandleFunc("POST /chats/{id}/read", a.chatRead)
	m.HandleFunc("GET /management/laboratories", a.labManagement)
	m.HandleFunc("POST /management/laboratories/catalog", a.saveLabCatalog)
	m.HandleFunc("POST /management/laboratories/payments/{id}", a.recordLabPayment)
	m.HandleFunc("GET /bookings/{id}/laboratory", a.labOrderPage)
	m.HandleFunc("POST /bookings/{id}/laboratory", a.labOrderAction)
	m.HandleFunc("GET /bookings/{id}/laboratory/{report}", a.labReportDownload)
	m.HandleFunc("GET /management/fulfillment", a.fulfillmentSettingsPage)
	m.HandleFunc("POST /management/fulfillment", a.saveFulfillmentSettings)
	m.HandleFunc("GET /wallet", a.walletPage)
	m.HandleFunc("GET /wallet/export.csv", a.walletCSV)
	m.HandleFunc("POST /wallet/bank", a.walletBankSave)
	m.HandleFunc("POST /wallet/withdrawals", a.walletWithdraw)
	m.HandleFunc("POST /wallet/withdrawals/{id}/cancel", a.walletCancel)
	m.HandleFunc("POST /wallet/topup", a.walletDemoTopup)
	m.HandleFunc("GET /wallet/receipts/{id}", a.walletReceipt)
	m.HandleFunc("GET /management/wallet", a.walletAdmin)
	m.HandleFunc("GET /management/wallet/export.csv", a.walletCSV)
	m.HandleFunc("POST /management/wallet/banks/{id}", a.walletBankReview)
	m.HandleFunc("POST /management/wallet/users/{id}/adjust", a.walletAdjust)
	m.HandleFunc("POST /management/wallet/users/{id}/freeze", a.walletFreeze)
	m.HandleFunc("POST /management/wallet/settings", a.walletSettingsSave)
	m.HandleFunc("POST /management/wallet/withdrawals/{id}", a.walletWithdrawalAction)
	m.HandleFunc("GET /management/questionnaires", a.formsAdmin)
	m.HandleFunc("POST /management/questionnaires/{scope}", a.saveQuestions)
	m.HandleFunc("GET /profile", a.ownerProfile)
	m.HandleFunc("POST /profile", a.saveOwnerProfile)
	m.HandleFunc("GET /specialists", a.providerDirectory)
	m.HandleFunc("GET /management/discovery", a.discoveryAdmin)
	m.HandleFunc("POST /management/discovery/settings/{service}", a.saveDiscovery)
	m.HandleFunc("POST /management/discovery/providers/{id}", a.saveDiscoveryFacts)
	m.HandleFunc("GET /specialists/{id}", a.specialistPage)
	m.HandleFunc("GET /provider/profile", a.specialistEditor)
	m.HandleFunc("POST /provider/profile", a.saveSpecialist)
	m.HandleFunc("GET /provider-media/{id}", a.specialistMedia)
	m.HandleFunc("POST /provider/media", a.uploadSpecialistMedia)
	m.HandleFunc("POST /provider/media/{id}/delete", a.deleteSpecialistMedia)
	m.HandleFunc("GET /management/support-content", a.supportAdmin)
	m.HandleFunc("GET /management/reviews", a.reviewsAdmin)
	m.HandleFunc("POST /management/reviews/{id}", a.publishReview)
	m.HandleFunc("GET /content", a.contentAdmin)
	m.HandleFunc("POST /content/{kind}/{id}", a.saveContent)
	m.HandleFunc("GET /sitemap.xml", a.sitemap)
	m.HandleFunc("GET /robots.txt", a.robots)
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
	m.HandleFunc("POST /booking/{id}/pet", a.bookingPet)
	m.HandleFunc("POST /booking/{id}/pay", a.pay)
	m.HandleFunc("GET /content/library", a.editorialLibrary)
	m.HandleFunc("GET /content/edit/{kind}/{id}", a.editorialEditor)
	m.HandleFunc("GET /content/preview/{kind}/{id}", a.editorialPreview)
	m.HandleFunc("POST /content/preview/{kind}/{id}", a.editorialPreview)
	m.HandleFunc("POST /content/magazine-layout", a.saveMagazineLayout)
	m.HandleFunc("POST /content/media", a.editorialUpload)
	m.HandleFunc("GET /editorial-media/{id}", a.editorialAsset)
	m.HandleFunc("GET /login", a.loginPage)
	m.HandleFunc("GET /provider/login", a.providerLogin)
	m.HandleFunc("POST /auth/google", a.googleStart)
	m.HandleFunc("GET /auth/google/callback", a.googleCallback)
	m.HandleFunc("GET /applications/{id}/documents", a.applicationDocuments)
	m.HandleFunc("POST /applications/{id}/documents", a.uploadApplicationDocument)
	m.HandleFunc("GET /applications/{id}/documents/{document}", a.applicationDocumentDownload)
	m.HandleFunc("POST /auth/request", a.requestOTP)
	m.HandleFunc("POST /auth/verify", a.verifyOTP)
	m.HandleFunc("POST /logout", a.logout)
	m.HandleFunc("GET /dashboard", a.dashboard)
	m.HandleFunc("GET /bookings/{id}", a.detail)
	m.HandleFunc("POST /bookings/{id}/cancel", a.cancel)
	m.HandleFunc("POST /bookings/{id}/replacement", a.replacement)
	m.HandleFunc("POST /bookings/{id}/review", a.review)
	m.HandleFunc("POST /pets", a.addPet)
	m.HandleFunc("GET /chats", a.chatArchive)
	m.HandleFunc("GET /chats/{id}", a.chatPage)
	m.HandleFunc("GET /chats/{id}/messages", a.chatPoll)
	m.HandleFunc("POST /chats/{id}/messages", a.chatSend)
	m.HandleFunc("POST /chats/{id}/end", a.chatEnd)
	m.HandleFunc("GET /chats/{id}/questionnaire", a.chatQuestionnairePage)
	m.HandleFunc("POST /chats/{id}/questionnaire", a.chatQuestionnaire)
	m.HandleFunc("GET /chat-media/{id}", a.chatMedia)
	m.HandleFunc("GET /management/chats", a.chatArchive)
	m.HandleFunc("GET /management/chats/{id}", func(w http.ResponseWriter, r *http.Request) {
		if a.auth(w, r, "admin", "ops") {
			a.chatPage(w, r)
		}
	})
	m.HandleFunc("POST /management/chats/{id}/duration", a.changeChatDuration)
	m.HandleFunc("GET /ops", a.ops)
	m.HandleFunc("POST /ops/bookings/{id}", a.opsAction)
	m.HandleFunc("POST /ops/availability", a.addAvailability)
	m.HandleFunc("POST /ops/applications/{id}", a.applicationDecision)
	m.HandleFunc("POST /ops/tickets/{id}", a.replyTicket)
	m.HandleFunc("GET /management/coverage", a.coveragePage)
	m.HandleFunc("POST /management/coverage/{id}", a.saveCoverage)
	m.HandleFunc("GET /provider/pricing", a.providerPricing)
	m.HandleFunc("POST /provider/pricing", a.saveProviderPricing)
	m.HandleFunc("GET /provider", a.providerPanel)
	m.HandleFunc("POST /provider/bookings/{id}", a.providerAction)
	m.HandleFunc("POST /provider/availability", a.providerAvailability)
	m.HandleFunc("GET /join", a.join)
	m.HandleFunc("POST /join", a.apply)
	m.HandleFunc("GET /support", a.support)
	m.HandleFunc("POST /support", a.ticket)
	m.HandleFunc("GET /management", a.admin)
	m.HandleFunc("GET /management/login", a.managementLogin)
	m.HandleFunc("GET /management/operations", func(w http.ResponseWriter, r *http.Request) {
		if a.auth(w, r, "admin") {
			a.ops(w, r)
		}
	})
	m.HandleFunc("POST /management/logout", a.managementLogout)
	managedAction := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if a.auth(w, r, "admin") {
				h(w, r)
			}
		}
	}
	m.HandleFunc("POST /management/operations/bookings/{id}", managedAction(a.opsAction))
	m.HandleFunc("POST /management/operations/availability", managedAction(a.addAvailability))
	m.HandleFunc("POST /management/operations/applications/{id}", managedAction(a.applicationDecision))
	m.HandleFunc("POST /management/operations/tickets/{id}", managedAction(a.replyTicket))
	m.HandleFunc("GET /management/{section}", a.admin)
	m.HandleFunc("POST /management/{kind}/{id}", a.businessSave)
	m.HandleFunc("POST /management/payout/{id}", a.businessPayout)
	m.HandleFunc("POST /management/integrations/{id}", a.saveIntegration)
	m.HandleFunc("GET /management/export.csv", a.businessExport)
	m.HandleFunc("GET /management/reports.csv", a.reportExport)
	m.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) { redirect(w, r, "/management") })
	m.HandleFunc("GET /admin/{section}", func(w http.ResponseWriter, r *http.Request) {
		redirect(w, r, "/management/"+r.PathValue("section")+"?"+r.URL.RawQuery)
	})
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
	policy := a.otpPolicy(r.Context())
	p := Page{OTPResend: policy.Resend, IsManagement: strings.HasPrefix(r.URL.Path, "/management"), CodeDigits: policy.Digits, CodeTTL: policy.TTL, Questions: a.questions(r.Context()), Title: title, Path: r.URL.Path, CSRF: s.CSRF, User: s.User, Services: Services, Areas: Areas, Menu: GroomMenu, Samples: Samples, Notice: r.URL.Query().Get("notice")}
	p.UnreadNotifications = a.notificationCount(r)
	a.livePage(r.Context(), &p)
	a.applySEO(r, &p)
	return p
}
func (a *App) render(w http.ResponseWriter, r *http.Request, name string, p Page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if p.User != nil || strings.HasPrefix(r.URL.Path, "/booking") || r.URL.Path == "/login" || name == "login" || name == "otp" || strings.HasPrefix(name, "google-") || strings.HasPrefix(name, "management-") {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	}
	var b strings.Builder
	if err := a.Templates.ExecuteTemplate(&b, name, p); err != nil {
		log.Printf("render: %v", err)
		http.Error(w, "خطای نمایش صفحه", 500)
		return
	}
	component := templ.Raw(b.String())
	if name != "editorial-preview" && (strings.HasPrefix(r.URL.Path, "/management") || strings.HasPrefix(name, "management-") || name == "business" || strings.HasPrefix(r.URL.Path, "/content")) {
		ui.ManagementDocument(p.Title, component).Render(r.Context(), w)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Retarget", "body")
		w.Header().Set("HX-Reswap", "innerHTML")
		component.Render(r.Context(), w)
		return
	}
	ui.PublicDocument(p.Title, p.SEO.Title, p.SEO.Description, p.SEO.Canonical, component).Render(r.Context(), w)
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	name := "error"
	if strings.HasPrefix(r.URL.Path, "/management") || strings.HasPrefix(r.FormValue("next"), "/management") {
		name = "management-error"
	}
	a.render(w, r, name, p)
}
func (a *App) auth(w http.ResponseWriter, r *http.Request, roles ...string) bool {
	u := sess(r).User
	if u == nil {
		login := "/login"
		if strings.HasPrefix(r.URL.Path, "/provider") || r.URL.Path == "/join" {
			login = "/provider/login"
		}
		if strings.HasPrefix(r.URL.Path, "/management") || strings.HasPrefix(r.URL.Path, "/admin") {
			login = "/management/login"
		}
		redirect(w, r, login+"?next="+r.URL.Path)
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
	for i := range p.Providers {
		if s.ID == "vet" {
			if q, e := a.quote(r.Context(), Booking{Service: "vet", ProviderID: p.Providers[i].ID}); e == nil {
				p.Providers[i].Price = q.Total
			}
		}
	}
	a.render(w, r, "landing", p)
}
func (a *App) providers(ctx context.Context, svc string) ([]Provider, error) {
	rows, err := a.DB.Query(ctx, "SELECT id,COALESCE(user_id,''),name,service,bio,license,avatar,verified,active,species,areas,price,experience,COALESCE((SELECT id FROM provider_media m WHERE m.provider_id=providers.id AND m.kind='portrait'),'') FROM providers WHERE active AND ($1='' OR service=$1) ORDER BY id", svc)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ps []Provider
	for rows.Next() {
		var p Provider
		if err = rows.Scan(&p.ID, &p.UserID, &p.Name, &p.Service, &p.Bio, &p.License, &p.Avatar, &p.Verified, &p.Active, &p.Species, &p.Areas, &p.Price, &p.Experience, &p.Portrait); err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	return ps, rows.Err()
}

const bookingColumns = "id,session_token,COALESCE(owner_id,''),service,stage,pet_name,species,age,weight,answers,area,address,COALESCE(provider_id,''),COALESCE(availability_id,''),items,sample,consent,amount,status,payment_status,payment_key,created_at,paid_at,exact_time,meeting_url,prescription,result,COALESCE(replacement_id,''),refund_amount,refund_reason,refund_status,notes,wallet_cash_used,wallet_bonus_used,COALESCE(provider_due,amount-amount*commission_bps/10000),request_flow,requires_ops_approval,stage_deadline,request_reason,time_flow,COALESCE(duration_minutes,0),fulfillment_flow,attendance_verified_at,settlement_approved_at,fulfillment_disputed,lab_flow,lab_cost,COALESCE(followup_id,'')"

func scanBooking(row pgx.Row) (Booking, error) {
	var b Booking
	err := row.Scan(&b.ID, &b.SessionToken, &b.OwnerID, &b.Service, &b.Stage, &b.PetName, &b.Species, &b.Age, &b.Weight, &b.Answers, &b.Area, &b.Address, &b.ProviderID, &b.AvailabilityID, &b.Items, &b.Sample, &b.Consent, &b.Amount, &b.Status, &b.PaymentStatus, &b.PaymentKey, &b.CreatedAt, &b.PaidAt, &b.ExactTime, &b.MeetingURL, &b.Prescription, &b.Result, &b.ReplacementID, &b.RefundAmount, &b.RefundReason, &b.RefundStatus, &b.Notes, &b.WalletCashUsed, &b.WalletBonusUsed, &b.ProviderDue, &b.RequestFlow, &b.RequiresOpsApproval, &b.StageDeadline, &b.RequestReason, &b.TimeFlow, &b.DurationMinutes, &b.FulfillmentFlow, &b.AttendanceVerifiedAt, &b.SettlementApprovedAt, &b.FulfillmentDisputed, &b.LabFlow, &b.LabCost, &b.FollowupID)
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
