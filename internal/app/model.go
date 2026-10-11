package app

import (
	"fmt"
	"strings"
	"time"
)

type QuestionOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}
type Question struct {
	ID       string           `json:"id"`
	Label    string           `json:"label"`
	Type     string           `json:"type"`
	Required bool             `json:"required"`
	When     string           `json:"when"`
	Options  []QuestionOption `json:"options"`
}
type User struct{ ID, Phone, Name, Role string }
type Session struct {
	Token, CSRF string
	User        *User
}
type Service struct {
	ID, Name, Short, Description, Icon, Color string
	Price                                     int64
}

var Services = []Service{{"vet", "دامپزشک آنلاین", "مشاوره دامپزشکی", "یک گفت‌وگوی مطمئن، برای روزهایی که نگرانش هستید.", "stethoscope", "mint", 350000}, {"grooming", "گرومینگ در منزل", "گرومینگ و پیرایش", "تمیز، مرتب و خوشحال؛ در آرامش خانهٔ خودتان.", "scissors", "peach", 250000}, {"sampling", "نمونه‌برداری در منزل", "آزمایش و نمونه‌برداری", "بدون استرس رفت‌وآمد؛ از نمونه‌گیری تا دریافت نتیجه.", "test", "lavender", 450000}}

type Provider struct {
	ID, UserID, Name, Service, Bio, License, Avatar string
	Verified, Active                                bool
	Species, Areas                                  []string
	Price                                           int64
	Experience                                      int
}
type Booking struct {
	ID, SessionToken, OwnerID, Service, PetName, Species, Age, Weight, Area, Address, ProviderID, AvailabilityID, Sample, Status, PaymentStatus, PaymentKey, MeetingURL, Prescription, Result, ReplacementID, RefundReason, RefundStatus, Notes string
	Stage                                                                                                                                                                                                                                       int
	Answers                                                                                                                                                                                                                                     map[string]string
	Items                                                                                                                                                                                                                                       []string
	Consent                                                                                                                                                                                                                                     bool
	Amount, RefundAmount                                                                                                                                                                                                                        int64
	CreatedAt                                                                                                                                                                                                                                   time.Time
	PaidAt, ExactTime                                                                                                                                                                                                                           *time.Time
}
type Slot struct {
	Used int

	ID, ProviderID, Day, Period      string
	ProviderName, StartTime, EndTime string
	Capacity                         int
	Start                            time.Time
	End                              time.Time
}

func (s Slot) Label() string {
	if s.Period == "custom" {
		return "دلخواه · " + digits(s.StartTime) + " تا " + digits(s.EndTime)
	}
	return period(s.Period)
}
func (s Slot) Remaining() int {
	n := s.Capacity - s.Used
	if n < 0 {
		return 0
	}
	return n
}

type Area struct{ ID, Name, Path string }

var Areas = []Area{{"saadat", "سعادت‌آباد", "20,15 100,15 105,65 50,85 20,60"}, {"shahrak", "شهرک غرب", "20,65 50,90 110,75 120,135 40,140 15,110"}, {"vanak", "ونک", "110,45 180,45 200,95 130,110 110,75"}, {"yousef", "یوسف‌آباد", "130,115 200,100 205,165 145,170 120,140"}, {"niavaran", "نیاوران", "185,15 275,15 285,70 205,85 180,40"}}

type MenuItem struct {
	ID, Name string
	Price    int64
}

var GroomMenu = []MenuItem{{"bath", "شست‌وشو و خشک‌کردن", 350000}, {"haircut", "کوتاهی و مرتب‌کردن مو", 450000}, {"nails", "کوتاهی ناخن", 150000}, {"ears", "پاک‌سازی گوش", 100000}}
var Samples = []MenuItem{{"blood", "نمونهٔ خون", 650000}, {"urine", "نمونهٔ ادرار", 450000}, {"stool", "نمونهٔ مدفوع", 450000}}

type Event struct {
	Event, Detail string
	At            time.Time
}
type Application struct {
	ID, UserID, Service, Name, License, Bio, Area, Status, Reason string
	Price                                                         int64
}
type Ticket struct{ ID, Subject, Body, Reply, Status string }
type Page struct {
	AssignmentSlots map[string][]Slot
	Chat            *ChatView
	Chats           []ChatSummary
	OTPResend       int

	IsManagement bool

	CodeDigits int
	CodeTTL    int

	Posts []BusinessRecord

	Business *BusinessPage
	Banners  []BusinessRecord

	Questions                                                  []Question
	Title, Path, CSRF, Error, Notice, Next, Challenge, DevCode string
	User                                                       *User
	Services                                                   []Service
	Areas                                                      []Area
	Menu, Samples                                              []MenuItem
	Booking                                                    *Booking
	Bookings                                                   []Booking
	Providers                                                  []Provider
	Slots                                                      []Slot
	Events                                                     []Event
	Applications                                               []Application
	Tickets                                                    []Ticket
	Pets                                                       []map[string]string
	Service                                                    Service
	Content                                                    string
	Count                                                      int
	Total                                                      int64
}

func service(id string) Service {
	for _, s := range Services {
		if s.ID == id {
			return s
		}
	}
	return Service{}
}
func status(s string) string {
	m := map[string]string{"draft": "پیش‌نویس", "paid": "در انتظار هماهنگی", "confirmed": "تأیید عملیات", "assigned": "تخصیص داده‌شده", "accepted": "پذیرفته‌شده", "in_progress": "در حال انجام", "collected": "نمونه گرفته شد", "delivered": "تحویل آزمایشگاه", "completed": "تکمیل‌شده", "result_ready": "نتیجه آماده", "replacement_pending": "در انتظار پذیرش جایگزین", "cancel_requested": "درخواست کنسلی", "cancelled": "لغوشده", "pending": "در انتظار بررسی", "approved": "تأییدشده", "rejected": "نیاز به اصلاح", "open": "در انتظار پاسخ", "closed": "پاسخ داده‌شده", "pending_refund": "در انتظار بازگشت", "refunded": "بازگشت آزمایشی ثبت شد"}
	if v, ok := m[s]; ok {
		return v
	}
	return s
}
func digits(s string) string {
	r := strings.NewReplacer("0", "۰", "1", "۱", "2", "۲", "3", "۳", "4", "۴", "5", "۵", "6", "۶", "7", "۷", "8", "۸", "9", "۹")
	return r.Replace(s)
}
func normalize(s string) string {
	return strings.NewReplacer("۰", "0", "۱", "1", "۲", "2", "۳", "3", "۴", "4", "۵", "5", "۶", "6", "۷", "7", "۸", "8", "۹", "9", "٠", "0", "١", "1", "٢", "2", "٣", "3", "٤", "4", "٥", "5", "٦", "6", "٧", "7", "٨", "8", "٩", "9").Replace(strings.TrimSpace(s))
}
func money(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return digits(s)
}
func areaName(id string) string {
	for _, a := range Areas {
		if a.ID == id {
			return a.Name
		}
	}
	return "خارج از پوشش"
}
func period(p string) string {
	return map[string]string{"morning": "صبح · ۹ تا ۱۲", "noon": "ظهر · ۱۲ تا ۱۵", "afternoon": "عصر · ۱۵ تا ۱۸", "night": "شب · ۱۸ تا ۲۱"}[p]
}
func contains(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}
func refundable(b Booking, now time.Time, platform bool) int64 {
	if platform || b.ExactTime == nil || b.ExactTime.Sub(now) >= 6*time.Hour {
		return b.Amount
	}
	return b.Amount / 10
}
