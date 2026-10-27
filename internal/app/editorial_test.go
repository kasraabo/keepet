package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func editorialTestMP4() []byte {
	box := func(kind string, body []byte) []byte {
		raw := make([]byte, 8+len(body))
		binary.BigEndian.PutUint32(raw, uint32(len(raw)))
		copy(raw[4:], kind)
		copy(raw[8:], body)
		return raw
	}
	handler := append(make([]byte, 8), []byte("vide")...)
	movie := box("moov", box("trak", box("mdia", box("hdlr", handler))))
	return bytes.Join([][]byte{box("ftyp", []byte("isom\x00\x00\x00\x00isommp42")), movie, box("mdat", []byte{1, 2, 3, 4})}, nil)
}
func TestEditorialMediaValidation(t *testing.T) {
	if !validEditorialMP4(editorialTestMP4()) {
		t.Fatal("valid video container rejected")
	}
	for _, raw := range [][]byte{nil, []byte("<svg onload=alert(1)>"), []byte("%PDF-1.4\n%%EOF"), editorialTestMP4()[:35], append(editorialTestMP4(), 1)} {
		if validEditorialMP4(raw) {
			t.Fatal("malformed container accepted")
		}
	}
	for _, raw := range []string{"javascript:alert(1)", "https://evil.example/embed/abc", "https://www.youtube.com.evil.example/watch?v=abcdefghijk", "https://user:pass@www.youtube.com/watch?v=abcdefghijk", "https://www.aparat.com/v/abcde/evil"} {
		if editorialVideo(raw) != (EditorialVideo{}) {
			t.Fatal("unsafe embed", raw)
		}
	}
	for _, raw := range []string{"https://youtu.be/abcdefghijk", "https://www.youtube.com/watch?v=abcdefghijk&feature=share", "https://www.aparat.com/v/abcde"} {
		if editorialVideo(raw) == (EditorialVideo{}) {
			t.Fatal("valid video link", raw)
		}
	}
}
func TestEditorial(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	u, e := url.Parse(dsn)
	if e != nil || !strings.HasSuffix(u.Path, "_test") {
		t.Fatal("isolated test database required")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("APP_MODE", "development")
	t.Setenv("SETTINGS_KEY_FILE", filepath.Join(t.TempDir(), "settings.key"))
	t.Setenv("ADMIN_SETTINGS_KEY", "")
	ctx := context.Background()
	a, e := New(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := a.DB.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec("TRUNCATE users CASCADE;TRUNCATE business_config CASCADE;TRUNCATE magazine_layout;INSERT INTO magazine_layout(id) VALUES(1)")
	if e = a.seed(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() { exec("TRUNCATE magazine_layout;INSERT INTO magazine_layout(id) VALUES(1)") }()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	editor, owner, ops, vet, guest := newBrowser(t, srv), newBrowser(t, srv), newBrowser(t, srv), newBrowser(t, srv), newBrowser(t, srv)
	editor.login("09120000007")
	owner.login("09128881001")
	ops.login("09120000001")
	vet.login("09120000002")
	post := func(b *browser, path string, v url.Values, want int) string {
		t.Helper()
		h, c := b.post(path, v)
		if c != want {
			t.Fatalf("%s status %d want %d: %s", path, c, want, h)
		}
		return h
	}
	save := func(kind, id string, v url.Values, want int) string {
		t.Helper()
		rec, _ := a.editorialRecord(ctx, kind, id)
		if v == nil {
			v = url.Values{}
		}
		v.Set("revision", fmt.Sprint(rec.Revision))
		return post(editor, "/content/"+kind+"/"+id, v, want)
	}
	upload := func(b *browser, data []byte, want int) string {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		form.WriteField("csrf", b.csrf)
		file, _ := form.CreateFormFile("file", "test-file.mp4")
		file.Write(data)
		form.Close()
		req, _ := http.NewRequest("POST", srv.URL+"/content/media", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		req.Header.Set("Accept", "application/json")
		res, e := b.client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("upload %d want %d %s", res.StatusCode, want, raw)
		}
		if want != 200 {
			return ""
		}
		var asset map[string]string
		if json.Unmarshal(raw, &asset) != nil || asset["url"] == "" {
			t.Fatal("upload JSON", string(raw))
		}
		return asset["url"]
	}
	for _, b := range []*browser{owner, ops, vet} {
		for _, path := range []string{"/content/library", "/content/edit/article/new-secret", "/content/preview/article/new-secret"} {
			if _, c := b.get(path); c != 403 {
				t.Fatal("non-editor access", path, c)
			}
		}
		upload(b, []byte("invalid"), 403)
	}
	var img bytes.Buffer
	png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 12, 8)))
	cover := upload(editor, img.Bytes(), 200)
	movie := upload(editor, editorialTestMP4(), 200)
	upload(editor, []byte("<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>"), 400)
	upload(editor, editorialTestMP4()[:30], 400)
	for _, path := range []string{cover, movie} {
		if _, c := guest.get(path); c != 404 {
			t.Fatal("unpublished asset public")
		}
		if _, c := owner.get(path); c != 404 {
			t.Fatal("owner read unpublished asset")
		}
		if _, c := editor.get(path); c != 200 {
			t.Fatal("editor cannot read own asset")
		}
	}
	v := url.Values{"editorial": {"on"}, "name": {"یادداشت پیش‌نویس محرمانه"}, "body": {"متن <script>alert(1)</script>"}, "content_type": {"article"}, "cover": {cover}, "blocks": {`[{"kind":"text","text":"بخش دوم <img src=x onerror=alert(1)>"}]`}, "publication": {"draft"}}
	save("article", "draft-rich", v, 200)
	if h, c := vet.get("/provider"); c != 200 || strings.Contains(h, "یادداشت پیش‌نویس محرمانه") {
		t.Fatal("draft leaked to optional reviewer")
	}
	post(vet, "/provider/articles/draft-rich/review", url.Values{"revision": {"1"}}, 409)
	if _, c := guest.get("/magazine/draft-rich"); c != 404 {
		t.Fatal("draft public")
	}
	preview := post(editor, "/content/preview/article/draft-rich", url.Values{"editorial": {"on"}, "name": {"عنوان ذخیره‌نشده"}, "body": {"پیش‌نمایش ذخیره‌نشده"}, "cover": {cover}}, 200)
	if !strings.Contains(preview, "عنوان ذخیره‌نشده") || !strings.Contains(preview, "پیش‌نمایش خصوصی") {
		t.Fatal("unsaved preview absent")
	}
	rec, _ := a.editorialRecord(ctx, "article", "draft-rich")
	if rec.Data.Name != "یادداشت پیش‌نویس محرمانه" || rec.Revision != 1 {
		t.Fatal("preview mutated record")
	}
	req, _ := http.NewRequest("GET", srv.URL+"/content/preview/article/draft-rich", nil)
	res, e := editor.client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.Header.Get("Cache-Control") != "private, no-store" || !strings.Contains(res.Header.Get("X-Robots-Tag"), "noindex") {
		t.Fatal("private preview headers")
	}
	sitemap, _ := guest.get("/sitemap.xml")
	if strings.Contains(sitemap, "/magazine/draft-rich") {
		t.Fatal("draft in sitemap")
	}
	v.Set("publication", "published")
	save("article", "draft-rich", v, 200)
	public, c := guest.get("/magazine/draft-rich")
	if c != 200 || strings.Contains(public, "<script>alert(1)</script>") || !strings.Contains(public, "&lt;script&gt;") || !strings.Contains(public, "بخش دوم") {
		t.Fatal("rich publication or XSS boundary")
	}
	if _, c := guest.get(cover); c != 200 {
		t.Fatal("published cover inaccessible")
	}
	// A legacy text-only editor must not erase previously saved media or blocks.
	save("article", "draft-rich", url.Values{"name": {"یادداشت منتشرشده"}, "body": {"متن تازه"}, "enabled": {"on"}}, 200)
	rec, _ = a.editorialRecord(ctx, "article", "draft-rich")
	if rec.Data.Cover != cover || len(rec.Data.Blocks) != 1 {
		t.Fatal("legacy form erased media")
	}
	for _, tc := range []struct {
		id, name string
		enabled  bool
	}{{"video-a", "ویدئوی اول", true}, {"video-b", "ویدئوی دوم", true}, {"video-c", "ویدئوی پیش‌نویس", false}} {
		state := "draft"
		if tc.enabled {
			state = "published"
		}
		save("article", tc.id, url.Values{"editorial": {"on"}, "name": {tc.name}, "content_type": {"video"}, "video": {movie}, "category": {"رفتار"}, "publication": {state}}, 200)
	}
	save("article", "not-selected", url.Values{"name": {"محتوای منتشرشدهٔ خارج از چیدمان"}, "body": {"متن"}, "enabled": {"on"}}, 200)
	layout := func(v url.Values, want int) {
		t.Helper()
		l, e := a.magazineLayout(ctx)
		if e != nil {
			t.Fatal(e)
		}
		v.Set("revision", fmt.Sprint(l.Revision))
		post(editor, "/content/magazine-layout", v, want)
	}
	layout(url.Values{"first": {"video-b", "video-a"}, "second": {"video-c"}, "articles": {"draft-rich"}}, 200)
	home, c := guest.get("/pages/magazine")
	if c != 200 || strings.Index(home, "ویدئوی دوم") > strings.Index(home, "ویدئوی اول") || strings.Contains(home, "ویدئوی پیش‌نویس") || strings.Contains(home, "محتوای منتشرشدهٔ خارج از چیدمان") || !strings.Contains(home, "یادداشت منتشرشده") {
		t.Fatal("selected layout order/draft boundary")
	}
	filtered, _ := guest.get("/pages/magazine?category=" + url.QueryEscape("رفتار"))
	if strings.Contains(filtered, "یادداشت منتشرشده") {
		t.Fatal("category filter ignored")
	}
	layout(url.Values{"first": {"video-a", "video-a"}}, 400)
	layout(url.Values{"first": {"draft-rich"}}, 400)
	post(editor, "/content/magazine-layout", url.Values{"revision": {"1"}}, 409)
	save("article", "video-c", url.Values{"editorial": {"on"}, "name": {"ویدئوی پیش‌نویس"}, "content_type": {"video"}, "video": {movie}, "publication": {"published"}}, 200)
	home, _ = guest.get("/pages/magazine")
	if !strings.Contains(home, "ویدئوی پیش‌نویس") {
		t.Fatal("selected draft did not appear after publication")
	}
	// A type change cannot leave text cards inside the video rows.
	save("article", "video-b", url.Values{"editorial": {"on"}, "name": {"تبدیل به مقاله"}, "body": {"متن"}, "content_type": {"article"}, "publication": {"published"}}, 200)
	home, _ = guest.get("/pages/magazine")
	if strings.Contains(home, "تبدیل به مقاله") {
		t.Fatal("wrong content type in video row")
	}
	save("seo", "new-article-meta", url.Values{"name": {"عنوان جستجوی اختصاصی"}, "description": {"توضیح متای اختصاصی"}, "link": {"/magazine/video-b"}, "enabled": {"on"}}, 200)
	h, c := guest.get("/magazine/video-b")
	if c != 200 || !strings.Contains(h, "<title>عنوان جستجوی اختصاصی · کیپت</title>") || !strings.Contains(h, "تبدیل به مقاله") || strings.Contains(h, "editorial-seo") {
		t.Fatal("custom article metadata ignored or empty SEO block displayed")
	}
	// HTTP byte ranges allow video seeking; unpublished videos become inaccessible immediately.
	req, _ = http.NewRequest("GET", srv.URL+movie, nil)
	req.Header.Set("Range", "bytes=0-15")
	res, e = guest.client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 206 || len(raw) != 16 || res.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("media range response")
	}
	for _, id := range []string{"video-a", "video-c"} {
		save("article", id, url.Values{"name": {"پیش‌نویس ویدئو"}, "body": {"متن"}, "publication": {"draft"}}, 200)
	}
	if _, c := guest.get(movie); c != 404 {
		t.Fatal("withdrawn video still public")
	}
	// Unpublishing an article also revokes assets used only by that article.
	save("article", "draft-rich", url.Values{"name": {"نسخه خصوصی"}, "body": {"متن"}, "publication": {"draft"}}, 200)
	if _, c := guest.get(cover); c != 404 {
		t.Fatal("withdrawn cover still public")
	}
	for _, kind := range []string{"page", "seo"} {
		id := "new-rich-" + kind
		path := "/pages/" + id
		if kind == "seo" {
			id = "home"
			path = "/"
		}
		values := url.Values{"editorial": {"on"}, "name": {"متن و ویدئوی " + kind}, "body": {"توضیح سئو یا صفحه"}, "cover": {cover}, "video": {"https://youtu.be/abcdefghijk"}, "blocks": {`[{"kind":"image","url":"` + cover + `","alt":"توضیح تصویر","caption":"زیرنویس"}]`}, "publication": {"published"}, "link": {path}}
		save(kind, id, values, 200)
		h, c := guest.get(path)
		if c != 200 || !strings.Contains(h, "متن و ویدئوی "+kind) || !strings.Contains(h, "youtube-nocookie.com/embed/abcdefghijk") || !strings.Contains(h, "زیرنویس") {
			t.Fatal("rich public SEO/page", kind, c)
		}
	}
	for _, svc := range []string{"vet", "grooming"} {
		path := "/specialists?service=" + svc
		save("seo", "directory-"+svc, url.Values{"name": {"سئوی اختصاصی " + svc}, "body": {"توضیح انتخاب " + svc}, "link": {path}, "enabled": {"on"}}, 200)
		h, c := guest.get(path + "&q=test")
		if c != 200 || !strings.Contains(h, "توضیح انتخاب "+svc) || !strings.Contains(h, `rel="canonical" href="`+a.publicBase(ctx)+path+`"`) {
			t.Fatal("directory SEO canonical/content", svc, c)
		}
	}
	// The booking picker reuses managed public copy while the booking remains private/noindex.
	var st string
	for _, cookie := range owner.client.Jar.Cookies(mustParseOnboardingURL(t, srv.URL)) {
		if cookie.Name == "keepet_session" {
			st = cookie.Value
		}
	}
	for _, svc := range []string{"vet", "grooming"} {
		id := token()
		exec("INSERT INTO bookings(id,session_token,owner_id,service,payment_key,stage,pet_name,age) SELECT $1,$2,user_id,$3,$4,2,'پت آزمایشی','۲ سال' FROM sessions WHERE token=$2", id, st, svc, token())
		h, c := owner.get("/booking/" + id)
		if c != 200 || !strings.Contains(h, "توضیح انتخاب "+svc) {
			t.Fatal("private picker SEO missing", svc, c)
		}
	}
	for _, path := range []string{"/management", "/profile", "/booking/abc", "//evil.example", "/specialists?service=vet&next=/management"} {
		save("seo", "new-invalid", url.Values{"name": {"نام"}, "link": {path}, "body": {"متن"}}, 400)
	}
	save("seo", "new-duplicate", url.Values{"name": {"نام"}, "link": {"/"}, "body": {"متن"}, "enabled": {"on"}}, 409)
	for _, bad := range []url.Values{
		{"editorial": {"on"}, "name": {"نام"}, "body": {"متن"}, "cover": {"https://evil.example/image.jpg"}},
		{"editorial": {"on"}, "name": {"نام"}, "body": {"متن"}, "video": {"javascript:alert(1)"}},
		{"editorial": {"on"}, "name": {"نام"}, "body": {"متن"}, "video": {cover}},
		{"editorial": {"on"}, "name": {"نام"}, "body": {"متن"}, "cover": {"/editorial-media/" + strings.Repeat("0", 64)}},
		{"editorial": {"on"}, "name": {"نام"}, "blocks": {`[{"kind":"image","url":"/static/pets.svg","alt":""}]`}},
	} {
		save("article", "new-bad-media", bad, 400)
	}
	if _, e := a.editorialRecord(ctx, "article", "new-bad-media"); e == nil {
		t.Fatal("invalid save partially committed")
	}
	// Stale editors cannot overwrite one another, including concurrent first creation.
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, c := editor.post("/content/article/new-concurrent", url.Values{"revision": {"0"}, "name": {fmt.Sprint("نسخه ", n)}, "body": {"متن"}})
			results <- c
		}(i)
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for c := range results {
		counts[c]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("concurrent edit protection", counts)
	}
	var n int
	if e := a.DB.QueryRow(ctx, "SELECT count(*) FROM business_audit WHERE kind IN ('article','editorial_upload','magazine_layout')").Scan(&n); e != nil || n < 8 {
		t.Fatal("editorial audit missing")
	}
}
