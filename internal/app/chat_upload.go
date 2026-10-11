package app

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"unicode"

	_ "golang.org/x/image/webp"
)

type chatUpload struct {
	Data                 []byte
	Kind, Mime, Filename string
}

// Validate file contents rather than trusting the multipart MIME or extension.
func readChatUpload(r *http.Request) (*chatUpload, error) {
	if r.MultipartForm == nil {
		return nil, nil
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		return nil, nil
	}
	if len(files) != 1 {
		return nil, errors.New("در هر پیام یک فایل ارسال کنید")
	}
	fh := files[0]
	if fh.Size > 12<<20 {
		return nil, errors.New("فایل صوتی حداکثر ۱۲ مگابایت باشد")
	}
	f, err := fh.Open()
	if err != nil {
		return nil, errors.New("فایل قابل خواندن نیست")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (12<<20)+1))
	if err != nil || len(data) == 0 || len(data) > 12<<20 {
		return nil, errors.New("فایل خالی یا بیش از حد مجاز است")
	}
	u := &chatUpload{Data: data}
	conf, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err == nil && (format == "jpeg" || format == "png" || format == "webp") {
		if len(data) > 8<<20 {
			return nil, errors.New("تصویر حداکثر ۸ مگابایت باشد")
		}
		if conf.Width < 1 || conf.Height < 1 || int64(conf.Width)*int64(conf.Height) > 20_000_000 {
			return nil, errors.New("ابعاد تصویر بیش از حد مجاز است")
		}
		if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
			return nil, errors.New("محتوای تصویر معتبر نیست")
		}
		u.Kind = "image"
		u.Mime = "image/" + format
	} else {
		u.Kind = "audio"
		u.Mime = chatAudioType(data)
		if u.Mime == "" {
			return nil, errors.New("فقط تصویر JPEG، PNG، WebP یا صوت WebM، OGG، M4A، MP3، WAV ارسال کنید")
		}
	}
	name := filepath.Base(strings.ReplaceAll(fh.Filename, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	if len([]rune(name)) > 120 {
		name = string([]rune(name)[:120])
	}
	if name == "" || name == "." {
		name = "پیوست"
	}
	u.Filename = name
	return u, nil
}

func chatAudioType(data []byte) string {
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE" {
		var format, samples bool
		for off := 12; off+8 <= len(data); {
			n := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
			if n > len(data)-off-8 {
				return ""
			}
			tag := string(data[off : off+4])
			if tag == "fmt " && n >= 16 {
				format = true
			}
			if tag == "data" && n > 0 {
				samples = true
			}
			off += 8 + n + (n % 2)
		}
		if format && samples {
			return "audio/wav"
		}
		return ""
	}
	if len(data) > 32 && bytes.HasPrefix(data, []byte("OggS")) && (bytes.Contains(data, []byte("OpusHead")) || bytes.Contains(data, []byte("\x01vorbis"))) && !bytes.Contains(data, []byte("theora")) {
		return "audio/ogg"
	}
	if len(data) > 32 && bytes.HasPrefix(data, []byte{0x1a, 0x45, 0xdf, 0xa3}) && bytes.Contains(data[:min(len(data), 4096)], []byte("webm")) && (bytes.Contains(data, []byte("A_OPUS")) || bytes.Contains(data, []byte("A_VORBIS"))) && !bytes.Contains(data, []byte("V_VP")) && !bytes.Contains(data, []byte("V_AV1")) {
		return "audio/webm"
	}
	if audioMP4(data) {
		return "audio/mp4"
	}
	// Skip a bounded ID3 tag, then require a valid MPEG audio frame header.
	off := 0
	if len(data) >= 10 && string(data[:3]) == "ID3" {
		for _, b := range data[6:10] {
			if b&0x80 != 0 {
				return ""
			}
		}
		off = 10 + int(data[6])<<21 + int(data[7])<<14 + int(data[8])<<7 + int(data[9])
	}
	if off+4 <= len(data) {
		b := data[off : off+4]
		if b[0] == 0xff && b[1]&0xe0 == 0xe0 && b[1]&0x18 != 0x08 && b[1]&0x06 != 0 && b[2]&0xf0 != 0 && b[2]&0xf0 != 0xf0 && b[2]&0x0c != 0x0c {
			return "audio/mpeg"
		}
	}
	return ""
}

// Inspect ISO-BMFF handlers to accept audio-only M4A/MP4, including Safari recordings.
func audioMP4(data []byte) bool {
	if len(data) < 16 || string(data[4:8]) != "ftyp" {
		return false
	}
	audio, video := false, false
	var boxes func([]byte, int) bool
	boxes = func(b []byte, depth int) bool {
		if depth > 6 {
			return false
		}
		for len(b) > 0 {
			if len(b) < 8 {
				return false
			}
			size := uint64(binary.BigEndian.Uint32(b[:4]))
			header := 8
			if size == 1 {
				if len(b) < 16 {
					return false
				}
				size = binary.BigEndian.Uint64(b[8:16])
				header = 16
			} else if size == 0 {
				size = uint64(len(b))
			}
			if size < uint64(header) || size > uint64(len(b)) {
				return false
			}
			payload := b[header:int(size)]
			typ := string(b[4:8])
			switch typ {
			case "moov", "trak", "mdia":
				if !boxes(payload, depth+1) {
					return false
				}
			case "hdlr":
				if len(payload) < 12 {
					return false
				}
				switch string(payload[8:12]) {
				case "soun":
					audio = true
				case "vide":
					video = true
				}
			}
			b = b[int(size):]
		}
		return true
	}
	return boxes(data, 0) && audio && !video
}
