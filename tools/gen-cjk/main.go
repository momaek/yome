// Command gen-cjk builds the embedded CJK stroke asset from makemeahanzi.
//
// Input: graphics.txt from https://github.com/skishore/makemeahanzi — one JSON
// object per line with "character" and "medians" (per-stroke centerline point
// lists, exactly the single-line form pen injection needs; the outline
// "strokes" field is ignored).
//
// Output: assets/cjk/medians.bin.gz, a gzipped binary table:
//
//	magic "CJK1"
//	u32   character count
//	per character:
//	  u32 rune
//	  u8  stroke count
//	  per stroke: u8 point count, then per point u16 x, u16 y
//
// Coordinates are converted from makemeahanzi's y-up system (glyph top at
// y=900, descenders to y=-124) to y-down glyph space: y' = 900 - y, giving
// a 1024-wide, 1024-tall glyph box (same convention as hanzi-writer's
// translate(0,900) scale(1,-1) transform).
//
// Usage: go run ./tools/gen-cjk -in graphics.txt -out assets/cjk/medians.bin.gz
//
// The data derives from Arphic fonts; see assets/cjk/ARPHICPL.TXT.
package main

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"unicode/utf8"
)

type entry struct {
	Character string         `json:"character"`
	Medians   [][][2]float64 `json:"medians"` // a few medians carry .5 halves
}

func main() {
	in := flag.String("in", "graphics.txt", "path to makemeahanzi graphics.txt")
	out := flag.String("out", "assets/cjk/medians.bin.gz", "output path")
	flag.Parse()

	f, err := os.Open(*in)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	type glyph struct {
		r       rune
		strokes [][][2]uint16
	}
	var glyphs []glyph

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		var e entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			log.Fatalf("line %d: %v", line, err)
		}
		r, size := utf8.DecodeRuneInString(e.Character)
		if r == utf8.RuneError || size != len(e.Character) {
			log.Fatalf("line %d: character %q is not a single rune", line, e.Character)
		}
		if len(e.Medians) == 0 || len(e.Medians) > 255 {
			log.Fatalf("line %d: %q has %d strokes", line, e.Character, len(e.Medians))
		}
		g := glyph{r: r}
		for _, m := range e.Medians {
			if len(m) < 2 || len(m) > 255 {
				log.Fatalf("line %d: %q has a stroke with %d points", line, e.Character, len(m))
			}
			pts := make([][2]uint16, len(m))
			for i, p := range m {
				x, y := int(p[0]+0.5), 900-int(p[1]+0.5)
				if x < 0 {
					x = 0
				}
				if x > 1400 {
					log.Fatalf("line %d: %q x=%v out of range", line, e.Character, p[0])
				}
				if y < 0 || y > 1400 {
					log.Fatalf("line %d: %q y=%v out of range", line, e.Character, p[1])
				}
				pts[i] = [2]uint16{uint16(x), uint16(y)}
			}
			g.strokes = append(g.strokes, pts)
		}
		glyphs = append(glyphs, g)
	}
	if err := sc.Err(); err != nil {
		log.Fatal(err)
	}
	sort.Slice(glyphs, func(i, j int) bool { return glyphs[i].r < glyphs[j].r })

	of, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	zw, _ := gzip.NewWriterLevel(of, gzip.BestCompression)
	w := bufio.NewWriter(zw)

	w.WriteString("CJK1")
	binary.Write(w, binary.LittleEndian, uint32(len(glyphs)))
	for _, g := range glyphs {
		binary.Write(w, binary.LittleEndian, uint32(g.r))
		w.WriteByte(byte(len(g.strokes)))
		for _, s := range g.strokes {
			w.WriteByte(byte(len(s)))
			for _, p := range s {
				binary.Write(w, binary.LittleEndian, p[0])
				binary.Write(w, binary.LittleEndian, p[1])
			}
		}
	}
	if err := w.Flush(); err != nil {
		log.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		log.Fatal(err)
	}
	if err := of.Close(); err != nil {
		log.Fatal(err)
	}
	st, _ := os.Stat(*out)
	fmt.Printf("%s: %d glyphs, %d bytes gzipped\n", *out, len(glyphs), st.Size())
}
