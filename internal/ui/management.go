package ui

import (
	"context"
	"github.com/a-h/templ"
	"html"
	"io"
)

// Management has its own document and assets, without storefront navigation or PWA installation.
func ManagementDocument(title string, body templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		_, e := io.WriteString(w, `<!DOCTYPE html><html lang="fa" dir="rtl"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="robots" content="noindex,nofollow"><title>`+html.EscapeString(title)+` · مدیریت کیپت</title><link rel="icon" href="/static/icon.svg"><link rel="stylesheet" href="/static/style.css"><link rel="stylesheet" href="/static/management.css"><link rel="stylesheet" href="/static/editorial.css"><link rel="stylesheet" href="/static/service-management.css"><script src="/static/interface.js" defer></script><script src="/static/editorial.js" defer></script><script src="/static/numbers.js" defer></script><script src="/static/management.js" defer></script><script src="/static/forms.js" defer></script><script src="/static/calendar.js" defer></script></head><body class="management-body">`)
		if e != nil {
			return e
		}
		if e = body.Render(ctx, w); e != nil {
			return e
		}
		_, e = io.WriteString(w, `</body></html>`)
		return e
	})
}
