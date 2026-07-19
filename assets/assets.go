// Package assets holds the data files compiled into the daemon: the Hershey
// stroke font and the shipped xochitl UI maps.
package assets

import (
	"embed"
	_ "embed"
)

// FuturalJHF is the Hershey Simplex (futural) single-stroke font in JHF format.
//
//go:embed hershey/futural.jhf
var FuturalJHF string

// UIMaps holds one TOML UI map per calibrated firmware version, under ui/.
// These are defaults; a config file may override any section.
//
//go:embed ui
var UIMaps embed.FS

// CJKMedians is the gzipped binary table of per-stroke centerlines for 9500+
// Chinese characters, generated from makemeahanzi by tools/gen-cjk. The data
// derives from Arphic fonts — see cjk/ARPHICPL.TXT for its license.
//
//go:embed cjk/medians.bin.gz
var CJKMedians []byte
