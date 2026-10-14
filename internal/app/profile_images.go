package app

import (
	"bytes"
	"fmt"
	_ "golang.org/x/image/webp"
	"image"
	_ "image/jpeg"
	_ "image/png"
)

func validateProfileImage(raw []byte) error {
	c, _, e := image.DecodeConfig(bytes.NewReader(raw))
	if e != nil || c.Width < 1 || c.Height < 1 || int64(c.Width)*int64(c.Height) > 20000000 {
		return fmt.Errorf("تصویر معتبر و حداکثر ۲۰ مگاپیکسل انتخاب کنید")
	}
	_, _, e = image.Decode(bytes.NewReader(raw))
	if e != nil {
		return fmt.Errorf("تصویر ناقص است")
	}
	return nil
}
