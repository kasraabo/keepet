package app

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Gregorian storage is retained; every calendar presented to users is Persian.
func jalaliParts(t time.Time) (int, int, int) {
	gy, gm, gd := t.Year(), int(t.Month()), t.Day()
	gdm := []int{0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334}
	gy2 := gy
	if gm > 2 {
		gy2++
	}
	days := 355666 + 365*gy + (gy2+3)/4 - (gy2+99)/100 + (gy2+399)/400 + gd + gdm[gm-1]
	jy := -1595 + 33*(days/12053)
	days %= 12053
	jy += 4 * (days / 1461)
	days %= 1461
	if days > 365 {
		jy += (days - 1) / 365
		days = (days - 1) % 365
	}
	jm, jd := 1+days/31, 1+days%31
	if days >= 186 {
		jm = 7 + (days-186)/30
		jd = 1 + (days-186)%30
	}
	return jy, jm, jd
}
func jalaliDate(s string) string {
	t, e := time.Parse("2006-01-02", s)
	if e != nil {
		return s
	}
	y, m, d := jalaliParts(t)
	return digits(fmt.Sprintf("%04d/%02d/%02d", y, m, d))
}
func parseJalali(s string) (time.Time, error) {
	s = normalize(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "/", "-")
	parts := strings.Split(s, "-")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("تاریخ شمسی معتبر وارد کنید")
	}
	y, e := strconv.Atoi(parts[0])
	m, e2 := strconv.Atoi(parts[1])
	d, e3 := strconv.Atoi(parts[2])
	if e != nil || e2 != nil || e3 != nil {
		return time.Time{}, fmt.Errorf("تاریخ نامعتبر")
	}
	if y >= 1900 {
		return time.Parse("2006-01-02", s)
	}
	if y < 1200 || y > 1600 || m < 1 || m > 12 || d < 1 || d > 31 {
		return time.Time{}, fmt.Errorf("تاریخ شمسی نامعتبر")
	}
	jy := y + 1595
	days := -355668 + 365*jy + (jy/33)*8 + ((jy%33 + 3) / 4) + d
	if m < 7 {
		days += (m - 1) * 31
	} else {
		days += (m-7)*30 + 186
	}
	gy := 400 * (days / 146097)
	days %= 146097
	if days > 36524 {
		gy += 100 * ((days - 1) / 36524)
		days = (days - 1) % 36524
		if days >= 365 {
			days++
		}
	}
	gy += 4 * (days / 1461)
	days %= 1461
	if days > 365 {
		gy += (days - 1) / 365
		days = (days - 1) % 365
	}
	gd := days + 1
	months := []int{0, 31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	if gy%4 == 0 && (gy%100 != 0 || gy%400 == 0) {
		months[2] = 29
	}
	gm := 1
	for gm <= 12 && gd > months[gm] {
		gd -= months[gm]
		gm++
	}
	t := time.Date(gy, time.Month(gm), gd, 0, 0, 0, 0, time.UTC)
	yy, mm, dd := jalaliParts(t)
	if yy != y || mm != m || dd != d {
		return time.Time{}, fmt.Errorf("روز انتخاب‌شده در تقویم شمسی وجود ندارد")
	}
	return t, nil
}
func parseCalendar(s string, loc *time.Location) (time.Time, error) {
	t, e := parseJalali(s)
	if e != nil {
		return t, e
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc), nil
}
func petBirth(age, year string) (string, string, error) {
	age = strings.TrimSpace(age)
	year = normalize(strings.TrimSpace(year))
	if year == "" {
		if age == "" || len(age) > 100 {
			return age, year, fmt.Errorf("سال تولد یا سن تقریبی پت را وارد کنید")
		}
		return age, year, nil
	}
	y, e := strconv.Atoi(year)
	now, _, _ := jalaliParts(time.Now())
	if e != nil || y < now-60 || y > now {
		return age, year, fmt.Errorf("سال تولد شمسی معتبر وارد کنید")
	}
	if age == "" {
		age = digits(strconv.Itoa(now-y)) + " سال"
	}
	return age, year, nil
}
