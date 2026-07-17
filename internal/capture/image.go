package capture

import (
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
)

// Portrait screen dimensions, as the user sees them.
const (
	ScreenW = 1404
	ScreenH = 1872
)

// BlackThreshold is the gray level below which a pixel counts as ink. Paper is
// 255 and ink is 0, so anything in the lower half is a mark.
const BlackThreshold = 128

// Image reads a frame and rotates it into the portrait view.
func (fb *Framebuffer) Image() (*image.Gray, error) {
	raw, err := fb.Raw()
	if err != nil {
		return nil, err
	}
	return Rotate(raw), nil
}

// Rotate turns a raw landscape frame into the portrait image the user sees:
// 90 degrees clockwise plus a vertical flip (ffmpeg transpose=3).
func Rotate(raw []uint16) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, ScreenW, ScreenH))
	for y := 0; y < ScreenH; y++ {
		for x := 0; x < ScreenW; x++ {
			img.Pix[y*img.Stride+x] = toGray(raw[rawIndex(x, y)])
		}
	}
	return img
}

// CountInk counts pixels darker than BlackThreshold. It is the cheap page
// fingerprint: blank-page detection, ink verification after a write, and
// probe assertions all reduce to comparing these counts.
func CountInk(img *image.Gray) int {
	var n int
	for _, p := range img.Pix {
		if p < BlackThreshold {
			n++
		}
	}
	return n
}

// CountInkRect counts ink pixels inside r only.
func CountInkRect(img *image.Gray, r image.Rectangle) int {
	r = r.Intersect(img.Bounds())
	var n int
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if img.Pix[y*img.Stride+x] < BlackThreshold {
				n++
			}
		}
	}
	return n
}

// InkCount reads a frame and counts its ink pixels.
func (fb *Framebuffer) InkCount() (int, error) {
	img, err := fb.Image()
	if err != nil {
		return 0, err
	}
	return CountInk(img), nil
}

// WritePNG encodes img as PNG to w.
func WritePNG(w io.Writer, img image.Image) error {
	if err := png.Encode(w, img); err != nil {
		return fmt.Errorf("encode png: %w", err)
	}
	return nil
}

// SavePNG writes img to a file.
func SavePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	if err := WritePNG(f, img); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}
