package schema

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // decoders for the photo formats people upload
	_ "image/jpeg"
	_ "image/png"
	"path"
	"strings"
)

// PhotoSize is the width and height every photo is shrunk to. Models see
// PhotoPixels numbers per photo: red, green and blue planes in turn.
const (
	PhotoSize   = 48
	PhotoPixels = 3 * PhotoSize * PhotoSize
)

// MaxPhotoBytes bounds one photo file.
const MaxPhotoBytes = 20 << 20

// ErrBadPhoto reports a file that isn't a photo the server can read.
var ErrBadPhoto = errors.New("schema: not a JPG, PNG or GIF photo")

// IsPhotoName reports whether a file name looks like a photo.
func IsPhotoName(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif":
		return true
	}
	return false
}

// DecodePhoto reads a JPG, PNG or GIF and returns its PhotoPixels bytes:
// the middle square of the photo, shrunk.
func DecodePhoto(b []byte) ([]uint8, error) {
	if len(b) > MaxPhotoBytes {
		return nil, fmt.Errorf("%w: bigger than %d MB", ErrBadPhoto, MaxPhotoBytes>>20)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, ErrBadPhoto
	}
	if cfg.Width*cfg.Height > 50_000_000 {
		return nil, fmt.Errorf("%w: more than 50 megapixels", ErrBadPhoto)
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, ErrBadPhoto
	}
	return Shrink(img), nil
}

// Shrink returns the middle square of img, averaged down to PhotoSize by
// PhotoSize, as red, green and blue planes of bytes.
func Shrink(img image.Image) []uint8 {
	r := img.Bounds()
	side := min(r.Dx(), r.Dy())
	x0, y0 := r.Min.X+(r.Dx()-side)/2, r.Min.Y+(r.Dy()-side)/2
	out := make([]uint8, PhotoPixels)
	if side == 0 {
		return out
	}
	const plane = PhotoSize * PhotoSize
	for py := range PhotoSize {
		ya, yb := y0+py*side/PhotoSize, y0+(py+1)*side/PhotoSize
		yb = max(yb, ya+1)
		for px := range PhotoSize {
			xa, xb := x0+px*side/PhotoSize, x0+(px+1)*side/PhotoSize
			xb = max(xb, xa+1)
			// Average at most 8×8 samples of the box, enough for a
			// smooth result without visiting every pixel of a big photo.
			sx, sy := max(1, (xb-xa)/8), max(1, (yb-ya)/8)
			var rs, gs, bs, n uint64
			for y := ya; y < yb; y += sy {
				for x := xa; x < xb; x += sx {
					cr, cg, cb, _ := img.At(x, y).RGBA()
					rs, gs, bs, n = rs+uint64(cr), gs+uint64(cg), bs+uint64(cb), n+1
				}
			}
			i := py*PhotoSize + px
			out[i] = uint8(rs / n >> 8)
			out[plane+i] = uint8(gs / n >> 8)
			out[2*plane+i] = uint8(bs / n >> 8)
		}
	}
	return out
}

// Floats turns photo bytes into values between 0 and 1, appended to dst.
func Floats(dst []float32, px []uint8) []float32 {
	for _, v := range px {
		dst = append(dst, float32(v)/255)
	}
	return dst
}
