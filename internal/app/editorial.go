package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

type EditorialBlock struct {
	Kind    string `json:"kind"`
	Text    string `json:"text,omitempty"`
	URL     string `json:"url,omitempty"`
	Alt     string `json:"alt,omitempty"`
	Caption string `json:"caption,omitempty"`
}
type EditorialVideo struct{ URL, Embed string }
type EditorialAsset struct{ ID, MIME string }
type MagazineLayout struct {
	Revision                int64
	First, Second, Articles []string
}
type EditorialPosition struct {
	Index    int
	Selected string
}
type EditorialPage struct {
	Record                           BusinessRecord
	Records, First, Second, Articles []BusinessRecord
	Assets                           []EditorialAsset
	Layout                           MagazineLayout
	Preview                          bool
	Category, Query                  string
	Categories                       []string
}

func editorialPosition(ids []string, n int) []EditorialPosition {
	out := make([]EditorialPosition, n)
	for i := range out {
		out[i].Index = i + 1
		if i < len(ids) {
			out[i].Selected = ids[i]
		}
	}
	return out
}

var editorialAssetID = regexp.MustCompile(`^/editorial-media/([a-f0-9]{64})$`)
var youtubeID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
var aparatID = regexp.MustCompile(`^[A-Za-z0-9]{5,16}$`)

func editorialVideo(raw string) EditorialVideo {
	if editorialAssetID.MatchString(raw) {
		return EditorialVideo{URL: raw}
	}
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return EditorialVideo{}
	}
	var id string
	switch u.Host {
	case "youtu.be":
		id = strings.TrimPrefix(u.Path, "/")
		if !youtubeID.MatchString(id) {
			return EditorialVideo{}
		}
	case "www.youtube.com", "youtube.com":
		if u.Path != "/watch" {
			return EditorialVideo{}
		}
		id = u.Query().Get("v")
		if !youtubeID.MatchString(id) {
			return EditorialVideo{}
		}
	case "www.aparat.com", "aparat.com":
		if !strings.HasPrefix(u.Path, "/v/") {
			return EditorialVideo{}
		}
		id = strings.TrimPrefix(u.Path, "/v/")
		if !aparatID.MatchString(id) {
			return EditorialVideo{}
		}
		return EditorialVideo{Embed: "https://www.aparat.com/video/video/embed/videohash/" + id + "/vt/frame"}
	default:
		return EditorialVideo{}
	}
	return EditorialVideo{Embed: "https://www.youtube-nocookie.com/embed/" + id + "?rel=0"}
}
func editorialSEOPath(r *http.Request) string {
	if r.URL.Path == "/specialists" {
		svc := r.URL.Query().Get("service")
		if svc == "" {
			svc = "vet"
		}
		return "/specialists?service=" + svc
	}
	return r.URL.Path
}
func editorialPublicPath(raw string) bool {
	parts := strings.Split(raw, "/")
	if len(parts) == 4 && parts[1] == "services" && service(parts[2]).ID != "" && businessID.MatchString(parts[3]) {
		return true
	}
	if contains([]string{"/", "/support", "/join", "/services/vet", "/services/grooming", "/services/sampling", "/specialists?service=vet", "/specialists?service=grooming"}, raw) {
		return true
	}
	for _, prefix := range []string{"/pages/", "/magazine/", "/specialists/"} {
		if strings.HasPrefix(raw, prefix) && businessID.MatchString(strings.TrimPrefix(raw, prefix)) {
			return true
		}
	}
	return false
}
func editorialImage(raw string) bool {
	return editorialAssetID.MatchString(raw) || contains([]string{"/static/pets.svg", "/static/icon.svg"}, raw)
}
func parseEditorial(r *http.Request, kind string, old BusinessData) (BusinessData, error) {
	d := old
	d.Name = trimmed(r.FormValue("name"))
	d.Body = trimmed(r.FormValue("body"))
	d.Enabled = r.FormValue("enabled") == "on"
	if r.FormValue("publication") == "published" {
		d.Enabled = true
	}
	if r.FormValue("publication") == "draft" {
		d.Enabled = false
	}
	if _, ok := r.Form["description"]; ok {
		d.Description = trimmed(r.FormValue("description"))
	}
	if kind == "seo" {
		d.Link = trimmed(r.FormValue("link"))
	}
	if r.FormValue("editorial") == "on" {
		d.Cover = trimmed(r.FormValue("cover"))
		d.Video = trimmed(r.FormValue("video"))
		d.Category = trimmed(r.FormValue("category"))
		d.ContentType = r.FormValue("content_type")
		if kind != "article" {
			d.ContentType = ""
		} else if d.ContentType == "" {
			d.ContentType = "article"
		}
		d.Blocks = nil
		if raw := r.FormValue("blocks"); raw != "" {
			if len(raw) > 100000 || json.Unmarshal([]byte(raw), &d.Blocks) != nil {
				return d, fmt.Errorf("ساختار بخش‌های محتوا نامعتبر است؛ صفحه را تازه کنید")
			}
		}
	}
	if d.Name == "" || len(d.Name) > 300 || len(d.Body) > 50000 || len(d.Description) > 600 || len(d.Category) > 100 || len(d.Blocks) > 24 {
		return d, fmt.Errorf("عنوان لازم است؛ طول متن یا تعداد بخش‌ها بیش از حد مجاز است")
	}
	if kind == "seo" && !editorialPublicPath(d.Link) {
		return d, fmt.Errorf("مسیر سئو باید یک صفحهٔ عمومی یا فهرست پزشک/گرومر باشد")
	}
	if kind == "article" && !contains([]string{"", "article", "video"}, d.ContentType) {
		return d, fmt.Errorf("نوع محتوا باید مقاله یا ویدئو باشد")
	}
	if d.Cover != "" && !editorialImage(d.Cover) {
		return d, fmt.Errorf("تصویر را از کتابخانهٔ رسانه انتخاب کنید")
	}
	if len(d.Video) > 500 || d.Video != "" && editorialVideo(d.Video) == (EditorialVideo{}) {
		return d, fmt.Errorf("ویدئو باید فایل کتابخانه، لینک YouTube یا لینک aparat.com/v باشد")
	}
	hasContent := d.Body != "" || d.Video != ""
	hasVideo := d.Video != ""
	total := len(d.Body)
	for _, b := range d.Blocks {
		total += len(b.Text) + len(b.Alt) + len(b.Caption)
		if len(b.Text) > 20000 || len(b.Caption) > 600 || len(b.Alt) > 300 || len(b.URL) > 500 {
			return d, fmt.Errorf("متن یا توضیح یکی از بخش‌ها بیش از حد طولانی است")
		}
		switch b.Kind {
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				return d, fmt.Errorf("متن بخش خالی است")
			}
			hasContent = true
		case "image":
			if !editorialImage(b.URL) || strings.TrimSpace(b.Alt) == "" {
				return d, fmt.Errorf("برای تصویر، فایل کتابخانه و توضیح تصویر لازم است")
			}
			hasContent = true
		case "video":
			if editorialVideo(b.URL) == (EditorialVideo{}) {
				return d, fmt.Errorf("لینک ویدئوی یکی از بخش‌ها معتبر نیست")
			}
			hasContent = true
			hasVideo = true
		default:
			return d, fmt.Errorf("نوع بخش محتوا نامعتبر است")
		}
	}
	if total > 80000 {
		return d, fmt.Errorf("مجموع متن محتوا بیش از حد مجاز است")
	}
	if kind == "article" && d.Enabled && (!hasContent || d.ContentType == "video" && !hasVideo) {
		return d, fmt.Errorf("برای انتشار، متن یا ویدئو اضافه کنید؛ محتوای ویدئویی باید ویدئو داشته باشد")
	}
	// A past optional review describes only that exact content version.
	before := old
	before.Enabled = d.Enabled
	if string(mustJSON(before)) != string(mustJSON(d)) {
		d.ReviewedBy = ""
		d.ReviewedAt = nil
	}
	return d, nil
}
func (a *App) editorialRecord(ctx context.Context, kind, id string) (BusinessRecord, error) {
	rec := BusinessRecord{Kind: kind, ID: id}
	var raw []byte
	e := a.DB.QueryRow(ctx, "SELECT revision,data FROM business_config WHERE kind=$1 AND id=$2", kind, id).Scan(&rec.Revision, &raw)
	if e == nil {
		e = json.Unmarshal(raw, &rec.Data)
	}
	return rec, e
}
func (a *App) editorialEditor(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin", "content") {
		return
	}
	kind, id := r.PathValue("kind"), r.PathValue("id")
	if !contains([]string{"article", "seo", "page"}, kind) || !businessID.MatchString(id) {
		a.fail(w, r, "نوع یا شناسهٔ محتوا نامعتبر است", 400)
		return
	}
	rec, e := a.editorialRecord(r.Context(), kind, id)
	if e != nil && !strings.HasPrefix(id, "new-") {
		a.fail(w, r, "محتوا پیدا نشد", 404)
		return
	}
	if e != nil {
		rec = BusinessRecord{Kind: kind, ID: id}
		if kind == "article" {
			rec.Data.ContentType = "article"
		}
	}
	p := a.page(r, "ویرایش محتوا")
	p.Editorial = &EditorialPage{Record: rec}
	rows, e := a.DB.Query(r.Context(), "SELECT id,mime FROM editorial_assets ORDER BY created_at DESC LIMIT 40")
	if e != nil {
		a.fail(w, r, "رسانه‌ها بارگذاری نشدند", 500)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var asset EditorialAsset
		if e = rows.Scan(&asset.ID, &asset.MIME); e != nil {
			a.fail(w, r, "رسانه‌ها خوانده نشدند", 500)
			return
		}
		p.Editorial.Assets = append(p.Editorial.Assets, asset)
	}
	if rows.Err() != nil {
		a.fail(w, r, "رسانه‌ها خوانده نشدند", 500)
		return
	}
	a.render(w, r, "editorial-editor", p)
}
func (a *App) saveEditorial(w http.ResponseWriter, r *http.Request) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	// Serialize creation of a missing record too; there is no row to lock yet.
	_, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,19019))", kind+":"+id)
	var before []byte
	var rev int64
	if e == nil {
		e = tx.QueryRow(ctx, "SELECT data,revision FROM business_config WHERE kind=$1 AND id=$2 FOR UPDATE", kind, id).Scan(&before, &rev)
	}
	newRow := e == pgx.ErrNoRows
	if e != nil && !newRow {
		a.fail(w, r, "محتوا خوانده نشد", 500)
		return
	}
	expected, err := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	if err != nil || expected != rev {
		a.fail(w, r, "نسخهٔ محتوا تغییر کرده؛ پیش از ذخیره صفحه را تازه کنید", 409)
		return
	}
	var old BusinessData
	if !newRow && json.Unmarshal(before, &old) != nil {
		a.fail(w, r, "محتوا خوانده نشد", 500)
		return
	}
	d, e := parseEditorial(r, kind, old)
	if e != nil {
		a.fail(w, r, e.Error(), 400)
		return
	}
	if kind == "seo" && d.Enabled {
		_, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,19020))", d.Link)
		var duplicate bool
		if e == nil {
			e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM business_config WHERE kind='seo' AND id<>$1 AND data->>'link'=$2 AND data->>'enabled'='true')", id, d.Link).Scan(&duplicate)
		}
		if e != nil {
			a.fail(w, r, "مسیر سئو بررسی نشد", 500)
			return
		}
		if duplicate {
			a.fail(w, r, "برای این مسیر سئوی فعال دیگری وجود دارد؛ همان مورد را ویرایش کنید", 409)
			return
		}
	}
	after := mustJSON(d)
	if newRow {
		_, e = tx.Exec(ctx, "INSERT INTO business_config(kind,id,data) VALUES($1,$2,$3)", kind, id, after)
	} else {
		_, e = tx.Exec(ctx, "UPDATE business_config SET data=$3,revision=revision+1 WHERE kind=$1 AND id=$2", kind, id, after)
	}
	if e == nil {
		e = linkEditorialAssets(ctx, tx, kind, id, d)
	}
	if e != nil {
		a.fail(w, r, "رسانهٔ انتخاب‌شده موجود نیست یا نوع آن با بخش تطابق ندارد", 400)
		return
	}
	_, e = tx.Exec(ctx, "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,$2,$3,$4,$5)", sess(r).User.ID, kind, id, before, after)
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "ذخیره نشد", 500)
		return
	}
	next := "/content"
	if r.FormValue("editorial") == "on" {
		next = "/content/edit/" + kind + "/" + id
	}
	msg := "پیش‌نویس ذخیره شد؛ برای دیگران نمایش داده نمی‌شود."
	if d.Enabled {
		msg = "محتوا منتشر شد؛ تأیید دیگری لازم نیست."
	}
	redirect(w, r, noticeURL(next, msg))
}
func linkEditorialAssets(ctx context.Context, tx pgx.Tx, kind, id string, d BusinessData) error {
	refs := map[string]string{}
	add := func(raw, mime string) error {
		m := editorialAssetID.FindStringSubmatch(raw)
		if len(m) == 0 {
			return nil
		}
		if was := refs[m[1]]; was != "" && was != mime {
			return fmt.Errorf("media kind mismatch")
		}
		refs[m[1]] = mime
		return nil
	}
	if e := add(d.Cover, "image/jpeg"); e != nil {
		return e
	}
	if e := add(d.Video, "video/mp4"); e != nil {
		return e
	}
	for _, b := range d.Blocks {
		if b.Kind == "image" {
			if e := add(b.URL, "image/jpeg"); e != nil {
				return e
			}
		}
		if b.Kind == "video" {
			if e := add(b.URL, "video/mp4"); e != nil {
				return e
			}
		}
	}
	if _, e := tx.Exec(ctx, "DELETE FROM editorial_asset_links WHERE kind=$1 AND target=$2", kind, id); e != nil {
		return e
	}
	for aid, mime := range refs {
		var actual string
		if e := tx.QueryRow(ctx, "SELECT mime FROM editorial_assets WHERE id=$1 FOR SHARE", aid).Scan(&actual); e != nil || actual != mime {
			return fmt.Errorf("media kind mismatch")
		}
		if _, e := tx.Exec(ctx, "INSERT INTO editorial_asset_links(kind,target,asset_id) VALUES($1,$2,$3)", kind, id, aid); e != nil {
			return e
		}
	}
	return nil
}
func (a *App) editorialPreview(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin", "content") {
		return
	}
	kind, id := r.PathValue("kind"), r.PathValue("id")
	if !contains([]string{"article", "page", "seo"}, kind) || !businessID.MatchString(id) {
		a.fail(w, r, "محتوا نامعتبر است", 400)
		return
	}
	rec, e := a.editorialRecord(r.Context(), kind, id)
	if r.Method == "POST" {
		if e != nil {
			rec = BusinessRecord{Kind: kind, ID: id}
		}
		rec.Data, e = parseEditorial(r, kind, rec.Data)
	} else if e != nil {
		a.fail(w, r, "محتوا پیدا نشد", 404)
		return
	}
	if e != nil {
		a.fail(w, r, e.Error(), 400)
		return
	}
	p := a.page(r, "پیش‌نمایش خصوصی")
	p.SEO = SEOData{}
	p.SEOBody = ""
	p.SEORich = nil
	p.Editorial = &EditorialPage{Record: rec, Preview: true}
	p.Posts = []BusinessRecord{rec}
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Cache-Control", "private, no-store")
	// Preview uses the storefront document, never the management layout.
	a.render(w, r, "editorial-preview", p)
}
func (a *App) magazineLayout(ctx context.Context) (MagazineLayout, error) {
	var l MagazineLayout
	e := a.DB.QueryRow(ctx, "SELECT revision,first_row,second_row,articles FROM magazine_layout WHERE id=1").Scan(&l.Revision, &l.First, &l.Second, &l.Articles)
	return l, e
}
func (a *App) editorialLibrary(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin", "content") {
		return
	}
	p := a.page(r, "تحریریهٔ Keepet Mag")
	rs, e := a.records(r.Context(), "article")
	if e != nil {
		a.fail(w, r, "محتوا بارگذاری نشد", 500)
		return
	}
	layout, e := a.magazineLayout(r.Context())
	if e != nil {
		a.fail(w, r, "چیدمان بارگذاری نشد", 500)
		return
	}
	layout = defaultMagazineLayout(layout, rs)
	p.Editorial = &EditorialPage{Records: rs, Layout: layout}
	p.Next = "new-" + token()[:12]
	a.render(w, r, "editorial-library", p)
}
func (a *App) saveMagazineLayout(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin", "content") {
		return
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "چیدمان ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	var before MagazineLayout
	e = tx.QueryRow(ctx, "SELECT revision,first_row,second_row,articles FROM magazine_layout WHERE id=1 FOR UPDATE").Scan(&before.Revision, &before.First, &before.Second, &before.Articles)
	rev, err := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	if e != nil || err != nil || before.Revision != rev {
		a.fail(w, r, "چیدمان تغییر کرده؛ صفحه را تازه کنید", 409)
		return
	}
	l := MagazineLayout{Revision: rev + 1}
	seen := map[string]bool{}
	for _, group := range []struct {
		key   string
		max   int
		out   *[]string
		video bool
	}{{"first", 4, &l.First, true}, {"second", 4, &l.Second, true}, {"articles", 20, &l.Articles, false}} {
		ids := r.Form[group.key]
		if len(ids) > group.max {
			a.fail(w, r, "تعداد جایگاه‌ها بیش از حد مجاز است", 400)
			return
		}
		for _, id := range ids {
			if id == "" {
				continue
			}
			if seen[id] || !businessID.MatchString(id) {
				a.fail(w, r, "یک محتوا را در چند جایگاه تکرار نکنید", 400)
				return
			}
			seen[id] = true
			var typ string
			e = tx.QueryRow(ctx, "SELECT COALESCE(data->>'content_type','article') FROM business_config WHERE kind='article' AND id=$1 FOR SHARE", id).Scan(&typ)
			if e != nil || ((typ == "video") != group.video) {
				a.fail(w, r, "محتوای انتخاب‌شده با نوع ردیف تطابق ندارد", 400)
				return
			}
			*group.out = append(*group.out, id)
		}
	}
	if l.First == nil {
		l.First = []string{}
	}
	if l.Second == nil {
		l.Second = []string{}
	}
	if l.Articles == nil {
		l.Articles = []string{}
	}
	_, e = tx.Exec(ctx, "UPDATE magazine_layout SET revision=revision+1,first_row=$1,second_row=$2,articles=$3 WHERE id=1", l.First, l.Second, l.Articles)
	if e == nil {
		_, e = tx.Exec(ctx, "INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,'magazine_layout','home',$2,$3)", sess(r).User.ID, mustJSON(before), mustJSON(l))
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "چیدمان ذخیره نشد", 500)
		return
	}
	redirect(w, r, noticeURL("/content/library", "چیدمان ذخیره شد؛ موارد پیش‌نویس تا انتشار نمایش داده نمی‌شوند."))
}
func (a *App) magazine(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "Keepet Mag · مجلهٔ کیپت")
	rs, e := a.records(r.Context(), "article")
	if e != nil {
		a.fail(w, r, "مجله بارگذاری نشد", 500)
		return
	}
	l, e := a.magazineLayout(r.Context())
	if e != nil {
		a.fail(w, r, "چیدمان بارگذاری نشد", 500)
		return
	}
	view := &EditorialPage{Layout: l, Category: strings.TrimSpace(r.URL.Query().Get("category")), Query: strings.TrimSpace(r.URL.Query().Get("q"))}
	byID := map[string]BusinessRecord{}
	l = defaultMagazineLayout(l, rs)
	for _, v := range rs {
		byID[v.ID] = v
	}
	categories := map[string]bool{}
	appendRow := func(ids []string, out *[]BusinessRecord, video bool) {
		for _, id := range ids {
			v, ok := byID[id]
			if !ok || !v.Data.Enabled || ((v.Data.ContentType == "video") != video) {
				continue
			}
			if v.Data.Category != "" && !categories[v.Data.Category] {
				view.Categories = append(view.Categories, v.Data.Category)
				categories[v.Data.Category] = true
			}
			if view.Category != "" && view.Category != v.Data.Category {
				continue
			}
			if view.Query != "" && !strings.Contains(strings.ToLower(v.Data.Name+" "+v.Data.Description+" "+v.Data.Category), strings.ToLower(view.Query)) {
				continue
			}
			*out = append(*out, v)
		}
	}
	appendRow(l.First, &view.First, true)
	appendRow(l.Second, &view.Second, true)
	appendRow(l.Articles, &view.Articles, false)
	p.Editorial = view
	a.render(w, r, "magazine", p)
}

func defaultMagazineLayout(l MagazineLayout, rs []BusinessRecord) MagazineLayout {
	if l.Revision != 1 {
		return l
	}
	for _, v := range rs {
		if !v.Data.Enabled {
			continue
		}
		if v.Data.ContentType == "video" {
			if len(l.First) < 4 {
				l.First = append(l.First, v.ID)
			} else if len(l.Second) < 4 {
				l.Second = append(l.Second, v.ID)
			}
		} else if len(l.Articles) < 20 {
			l.Articles = append(l.Articles, v.ID)
		}
	}
	return l
}

// The same managed copy can accompany the private booking picker without indexing a reservation.
func (a *App) editorialSelectionSEO(r *http.Request, p *Page, service string) {
	rs, e := a.records(r.Context(), "seo")
	if e != nil {
		return
	}
	for _, v := range rs {
		if v.Data.Enabled && v.Data.Link == "/specialists?service="+service {
			d := v.Data
			p.SEORich = &d
			p.SEOBody = d.Body
			p.SEO.Title = d.Name
			return
		}
	}
}
