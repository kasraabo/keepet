package app

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"strings"
	"time"
)

// Only well-formed ISO-BMFF MP4 containers with a video track are accepted.
// Files are served as media with nosniff, never as executable pages.
func validEditorialMP4(raw []byte) bool {
	ftyp, moov, mdat, video := false, false, false, false
	for off := 0; off < len(raw); {
		if len(raw)-off < 8 {
			return false
		}
		size := uint64(binary.BigEndian.Uint32(raw[off:]))
		header := uint64(8)
		kind := string(raw[off+4 : off+8])
		if size == 1 {
			if len(raw)-off < 16 {
				return false
			}
			size = binary.BigEndian.Uint64(raw[off+8:])
			header = 16
		}
		if size == 0 {
			size = uint64(len(raw) - off)
		}
		if size < header || size > uint64(len(raw)-off) {
			return false
		}
		body := raw[off+int(header) : off+int(size)]
		switch kind {
		case "ftyp":
			if off != 0 || len(body) < 8 {
				return false
			}
			for i := 0; i+4 <= len(body); i += 4 {
				if contains([]string{"isom", "iso2", "mp41", "mp42", "avc1", "M4V "}, string(body[i:i+4])) {
					ftyp = true
				}
			}
		case "moov":
			moov = true
			video = mp4HasVideo(body, 0)
		case "mdat":
			mdat = len(body) > 0
		}
		off += int(size)
	}
	return ftyp && moov && mdat && video
}
func mp4HasVideo(raw []byte, depth int) bool {
	if depth > 5 {
		return false
	}
	for off := 0; off < len(raw); {
		if len(raw)-off < 8 {
			return false
		}
		n := int(binary.BigEndian.Uint32(raw[off:]))
		if n < 8 || n > len(raw)-off {
			return false
		}
		kind := string(raw[off+4 : off+8])
		body := raw[off+8 : off+n]
		if kind == "hdlr" && len(body) >= 12 && string(body[8:12]) == "vide" {
			return true
		}
		if contains([]string{"trak", "mdia"}, kind) && mp4HasVideo(body, depth+1) {
			return true
		}
		off += n
	}
	return false
}
func (a *App) editorialUpload(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r, "admin", "content") {
		return
	}
	next := "/content/library"
	if p := r.FormValue("next"); len(p) < 200 && safeNext(p) == p && len(p) > len("/content/edit/") && p[:len("/content/edit/")] == "/content/edit/" {
		next = p
	}
	if r.MultipartForm == nil || len(r.MultipartForm.File["file"]) != 1 {
		a.fail(w, r, "یک فایل تصویر یا ویدئو انتخاب کنید", 400)
		return
	}
	fh := r.MultipartForm.File["file"][0]
	if fh.Size > 64<<20 {
		a.fail(w, r, "حجم ویدئو حداکثر ۶۴ مگابایت است", 413)
		return
	}
	f, e := fh.Open()
	if e != nil {
		a.fail(w, r, "فایل خوانده نشد", 400)
		return
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, (64<<20)+1))
	if e != nil || len(raw) == 0 || len(raw) > 64<<20 {
		a.fail(w, r, "فایل خالی یا بیش از حد مجاز است", 400)
		return
	}
	mime := "video/mp4"
	if !validEditorialMP4(raw) {
		if len(raw) > 8<<20 {
			a.fail(w, r, "تصویر حداکثر ۸ مگابایت و ویدئو باید MP4 دارای تصویر باشد", 400)
			return
		}
		c, kind, e := image.DecodeConfig(bytes.NewReader(raw))
		if e != nil || !contains([]string{"jpeg", "png", "webp"}, kind) || c.Width < 1 || c.Height < 1 || int64(c.Width)*int64(c.Height) > 20_000_000 {
			a.fail(w, r, "تصویر معتبر JPEG، PNG، WebP یا ویدئوی MP4 انتخاب کنید", 400)
			return
		}
		im, _, e := image.Decode(bytes.NewReader(raw))
		if e != nil {
			a.fail(w, r, "تصویر ناقص است", 400)
			return
		}
		var out bytes.Buffer
		if e = jpeg.Encode(&out, im, &jpeg.Options{Quality: 90}); e != nil || out.Len() > 8<<20 {
			a.fail(w, r, "تصویر را کوچک‌تر کنید", 400)
			return
		}
		raw = out.Bytes()
		mime = "image/jpeg"
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		a.fail(w, r, "رسانه ذخیره نشد", 500)
		return
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,19021))", sess(r).User.ID)
	var count int
	if e == nil {
		e = tx.QueryRow(ctx, "SELECT count(*) FROM editorial_assets WHERE created_by=$1 AND created_at>now()-interval '1 day'", sess(r).User.ID).Scan(&count)
	}
	if e != nil {
		a.fail(w, r, "رسانه ذخیره نشد", 500)
		return
	}
	if count >= 100 {
		a.fail(w, r, "سقف روزانهٔ بارگذاری رسانه تکمیل شده است", 429)
		return
	}
	id := token()
	_, e = tx.Exec(ctx, "INSERT INTO editorial_assets(id,created_by,mime,data) VALUES($1,$2,$3,$4)", id, sess(r).User.ID, mime, raw)
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO business_audit(actor_id,kind,target,before_data,after_data) VALUES($1,'editorial_upload',$2,'{}',$3)`, sess(r).User.ID, id, mustJSON(map[string]any{"mime": mime, "size": len(raw)}))
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		a.fail(w, r, "رسانه ذخیره نشد", 500)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"id": id, "url": "/editorial-media/" + id, "mime": mime})
		return
	}
	redirect(w, r, noticeURL(next, "رسانه بارگذاری شد؛ اکنون آن را از کتابخانه در محتوای خود انتخاب کنید."))
}
func (a *App) editorialAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !editorialAssetID.MatchString("/editorial-media/" + id) {
		a.fail(w, r, "رسانه پیدا نشد", 404)
		return
	}
	staff := sess(r).User != nil && contains([]string{"admin", "content"}, sess(r).User.Role)
	var raw []byte
	var mime string
	var at time.Time
	// Publication status is checked on every request, including byte-range requests.
	e := a.DB.QueryRow(r.Context(), `SELECT a.data,a.mime,a.created_at FROM editorial_assets a WHERE a.id=$1 AND ($2 OR EXISTS(SELECT 1 FROM editorial_asset_links l JOIN business_config c ON c.kind=l.kind AND c.id=l.target WHERE l.asset_id=a.id AND c.data->>'enabled'='true'))`, id, staff).Scan(&raw, &mime, &at)
	if e != nil {
		a.fail(w, r, "رسانه پیدا نشد", 404)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Robots-Tag", "noindex")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	ext := "jpg"
	if mime == "video/mp4" {
		ext = "mp4"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="keepet-media.%s"`, ext))
	http.ServeContent(w, r, "keepet-media."+ext, at, bytes.NewReader(raw))
}
