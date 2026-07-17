// preview — render an SVG file's paths to a local PNG for iteration
// before injecting. Not used on-device.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

func previewCmd(file, out string, canvasW int) {
	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var polys [][]fpt
	for _, m := range dAttrRe.FindAllStringSubmatch(string(data), -1) {
		polys = append(polys, parsePathD(m[1])...)
	}
	if len(polys) == 0 {
		fmt.Fprintln(os.Stderr, "no paths in", file)
		os.Exit(1)
	}
	minX, minY := polys[0][0].X, polys[0][0].Y
	maxX, maxY := minX, minY
	for _, p := range polys {
		for _, q := range p {
			minX, maxX = math.Min(minX, q.X), math.Max(maxX, q.X)
			minY, maxY = math.Min(minY, q.Y), math.Max(maxY, q.Y)
		}
	}
	pad := 10.0
	scale := (float64(canvasW) - 2*pad) / (maxX - minX)
	h := int((maxY-minY)*scale + 2*pad)
	img := image.NewGray(image.Rect(0, 0, canvasW, h))
	for y := 0; y < h; y++ {
		for x := 0; x < canvasW; x++ {
			img.SetGray(x, y, color.Gray{255})
		}
	}
	plot := func(x, y float64) {
		for dx := -1; dx <= 0; dx++ {
			for dy := -1; dy <= 0; dy++ {
				img.SetGray(int(x)+dx, int(y)+dy, color.Gray{0})
			}
		}
	}
	for _, poly := range polys {
		for i := 1; i < len(poly); i++ {
			x0, y0 := pad+(poly[i-1].X-minX)*scale, pad+(poly[i-1].Y-minY)*scale
			x1, y1 := pad+(poly[i].X-minX)*scale, pad+(poly[i].Y-minY)*scale
			n := int(math.Hypot(x1-x0, y1-y0)/0.5) + 1
			for j := 0; j <= n; j++ {
				t := float64(j) / float64(n)
				plot(x0+(x1-x0)*t, y0+(y1-y0)*t)
			}
		}
	}
	f, _ := os.Create(out)
	defer f.Close()
	png.Encode(f, img)
	fmt.Printf("preview: %d strokes -> %s (%dx%d)\n", len(polys), out, canvasW, h)
}
