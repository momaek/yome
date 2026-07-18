// Package penstate reads the user's last-used pen from xochitl's settings.
//
// xochitl persists the writing-tool state in xochitl.conf under the
// LastWritingTool key: a QSettings-escaped @Variant(...) payload holding a
// QDataStream-serialized QVariantMap (observed on firmware 3.27.3.0:
// twelve entries — LastPen, LastPenColor, LastPenSize, eraser and secondary
// pen counterparts). A session that switches the pen style on the model's
// behalf reads this state first and puts the user's pen back afterwards
// (plan 4.3 session etiquette; deferred from M3).
package penstate

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"strings"
)

// DefaultConfPath is where xochitl keeps its settings on the device.
const DefaultConfPath = "/home/root/.config/remarkable/xochitl.conf"

// confKey is the settings key holding the serialized writing-tool state.
const confKey = "LastWritingTool"

// State is the user's writing-tool state as xochitl last persisted it.
// Missing integer fields are -1 and missing sizes are 0: xochitl.conf is
// only rewritten when xochitl feels like it, so absent keys are survivable.
type State struct {
	Pen      int     // pen tool id (xochitl's .rm tool vocabulary)
	PenColor int     // color id (0 black, 1 gray, 2 white, 6 blue, 7 red, …)
	PenSize  float64 // 1 thin, 2 medium, 3 thick
}

// Read extracts the pen state from a xochitl.conf file.
func Read(path string) (State, error) {
	f, err := os.Open(path)
	if err != nil {
		return State{}, fmt.Errorf("penstate: %w", err)
	}
	defer f.Close()

	prefix := confKey + "=@Variant("
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		if !strings.HasSuffix(line, ")") {
			return State{}, fmt.Errorf("penstate: %s value is not a single-line @Variant", confKey)
		}
		blob, err := unescapeINI(line[len(prefix) : len(line)-1])
		if err != nil {
			return State{}, fmt.Errorf("penstate: %s: %w", confKey, err)
		}
		return parseVariantMap(blob)
	}
	if err := sc.Err(); err != nil {
		return State{}, fmt.Errorf("penstate: %w", err)
	}
	return State{}, fmt.Errorf("penstate: no %s key in %s", confKey, path)
}

// unescapeINI undoes QSettings' INI byte escaping: backslash sequences for
// control characters plus \xH… hex runs (terminated by the first non-hex
// character, which in practice is the backslash of the next escape).
func unescapeINI(s string) ([]byte, error) {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c != '\\' {
			out = append(out, c)
			i++
			continue
		}
		if i+1 >= len(s) {
			return nil, fmt.Errorf("dangling backslash at byte %d", i)
		}
		switch n := s[i+1]; n {
		case 'x':
			j := i + 2
			for j < len(s) && isHex(s[j]) {
				j++
			}
			if j == i+2 {
				return nil, fmt.Errorf("\\x with no hex digits at byte %d", i)
			}
			var v uint64
			for _, h := range []byte(s[i+2 : j]) {
				v = v<<4 | uint64(hexVal(h))
				if v > 0xFF {
					return nil, fmt.Errorf("\\x escape wider than a byte at %d", i)
				}
			}
			out = append(out, byte(v))
			i = j
		case '0':
			out = append(out, 0)
			i += 2
		case 'a':
			out = append(out, 7)
			i += 2
		case 'b':
			out = append(out, 8)
			i += 2
		case 't':
			out = append(out, 9)
			i += 2
		case 'n':
			out = append(out, 10)
			i += 2
		case 'v':
			out = append(out, 11)
			i += 2
		case 'f':
			out = append(out, 12)
			i += 2
		case 'r':
			out = append(out, 13)
			i += 2
		case '"', ';', ',', '\\', '\'':
			out = append(out, n)
			i += 2
		default:
			return nil, fmt.Errorf("unknown escape \\%c at byte %d", n, i)
		}
	}
	return out, nil
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func hexVal(c byte) int {
	switch {
	case c <= '9':
		return int(c - '0')
	case c >= 'a':
		return int(c-'a') + 10
	default:
		return int(c-'A') + 10
	}
}

// QDataStream type ids observed in the map's values.
const (
	qBool   = 1
	qInt    = 2
	qDouble = 6
	qMap    = 8
)

// parseVariantMap decodes the QVariantMap blob. Layout (as written by
// xochitl 3.27, byte-verified against a captured conf): u32 type id, then
// for the map a u32 entry count, then per entry a QString key (u32 byte
// length + UTF-16BE bytes) and a value (u32 type id + payload) — notably
// with no is-null bytes anywhere.
func parseVariantMap(blob []byte) (State, error) {
	st := State{Pen: -1, PenColor: -1}
	d := decoder{buf: blob}
	if t := d.u32(); t != qMap {
		return st, fmt.Errorf("penstate: @Variant type %d, want QVariantMap (%d)", t, qMap)
	}
	n := d.u32()
	if n > 1024 {
		return st, fmt.Errorf("penstate: implausible map size %d", n)
	}
	for range n {
		key := d.utf16String()
		var f float64
		switch t := d.u32(); t {
		case qBool:
			d.u8()
		case qInt:
			f = float64(int32(d.u32()))
		case qDouble:
			f = math.Float64frombits(d.u64())
		default:
			return st, fmt.Errorf("penstate: key %q has unsupported QVariant type %d", key, t)
		}
		if d.err != nil {
			break
		}
		switch key {
		case "LastPen":
			st.Pen = int(f)
		case "LastPenColor":
			st.PenColor = int(f)
		case "LastPenSize":
			st.PenSize = f
		}
	}
	if d.err != nil {
		return st, fmt.Errorf("penstate: %w", d.err)
	}
	return st, nil
}

// decoder is a bounds-checked big-endian reader; the first overrun poisons
// every later read, so call sites check err once at the end.
type decoder struct {
	buf []byte
	off int
	err error
}

func (d *decoder) take(n int) []byte {
	if d.err != nil || d.off+n > len(d.buf) {
		if d.err == nil {
			d.err = fmt.Errorf("truncated blob at byte %d", d.off)
		}
		return nil
	}
	b := d.buf[d.off : d.off+n]
	d.off += n
	return b
}

func (d *decoder) u8() byte {
	if b := d.take(1); b != nil {
		return b[0]
	}
	return 0
}

func (d *decoder) u32() uint32 {
	if b := d.take(4); b != nil {
		return binary.BigEndian.Uint32(b)
	}
	return 0
}

func (d *decoder) u64() uint64 {
	if b := d.take(8); b != nil {
		return binary.BigEndian.Uint64(b)
	}
	return 0
}

func (d *decoder) utf16String() string {
	n := int(d.u32())
	if n%2 != 0 || n > 4096 {
		if d.err == nil {
			d.err = fmt.Errorf("implausible string length %d at byte %d", n, d.off)
		}
		return ""
	}
	b := d.take(n)
	if b == nil {
		return ""
	}
	// Keys are plain ASCII stored as UTF-16BE; anything outside that would
	// not match a known key anyway, so surrogates need no special care.
	r := make([]rune, 0, n/2)
	for i := 0; i < n; i += 2 {
		r = append(r, rune(binary.BigEndian.Uint16(b[i:])))
	}
	return string(r)
}
