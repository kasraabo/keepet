package ui

import (
	"context"
	"github.com/a-h/templ"
	"html"
	"io"
)

func PublicDocument(title, seoTitle, description, canonical string, body templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if seoTitle != "" {
			title = seoTitle
		}
		head := `<!DOCTYPE html><html lang="fa" dir="rtl"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="theme-color" content="#153e35"><title>` + html.EscapeString(title) + ` · کیپت</title><meta name="description" content="` + html.EscapeString(description) + `"><meta property="og:title" content="` + html.EscapeString(title) + `"><meta property="og:description" content="` + html.EscapeString(description) + `"><meta property="og:type" content="website">`
		if canonical != "" {
			head += `<link rel="canonical" href="` + html.EscapeString(canonical) + `">`
		}
		head += `<link rel="manifest" href="/manifest.webmanifest"><link rel="icon" href="/static/icon.svg"><link rel="stylesheet" href="/static/style.css"><link rel="stylesheet" href="/static/editorial.css"><script src="/static/editorial.js" defer></script><script src="/static/numbers.js" defer></script><script src="/static/vendor/htmx.min.js" defer></script><script src="/static/vendor/alpine.min.js" defer></script><script src="/static/app.js" defer></script><script src="/static/forms.js" defer></script><script src="/static/calendar.js" defer></script></head><body>`
		if _, e := io.WriteString(w, head); e != nil {
			return e
		}
		if e := body.Render(ctx, w); e != nil {
			return e
		}
		_, e := io.WriteString(w, `</body></html>`)
		return e
	})
}
