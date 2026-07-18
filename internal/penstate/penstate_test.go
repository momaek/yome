package penstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realLine is the LastWritingTool line captured verbatim from a 3.27.3.0
// device (2026-07-18): ballpoint (15), black (0), medium size (2.0).
func realLine(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "lastwritingtool-3.27.3.0.line"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimRight(string(b), "\n")
}

func writeConf(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "xochitl.conf")
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadCapturedDeviceConf(t *testing.T) {
	path := writeConf(t,
		"[General]",
		"LastOpen=720d630f-d9f3-47a0-970f-13103570579e",
		realLine(t),
		"OnboardingUpdateScreen=false",
	)
	st, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Pen != 15 || st.PenColor != 0 || st.PenSize != 2.0 {
		t.Fatalf("got %+v, want Pen=15 PenColor=0 PenSize=2", st)
	}
	pen, size, color := st.Controls()
	if pen != "pen_ballpoint" || size != "size_medium" || color != "color_black" {
		t.Fatalf("Controls() = %q %q %q", pen, size, color)
	}
}

func TestReadMissingKey(t *testing.T) {
	path := writeConf(t, "[General]", "LastOpen=abc")
	if _, err := Read(path); err == nil {
		t.Fatal("want an error for a conf without LastWritingTool")
	}
}

func TestReadMissingFile(t *testing.T) {
	if _, err := Read(filepath.Join(t.TempDir(), "nope.conf")); err == nil {
		t.Fatal("want an error for a missing file")
	}
}

// TestReadTruncatedBlob guards the bounds-checked decoder: a cut-off value
// must surface as an error, never a panic or a silent zero state.
func TestReadTruncatedBlob(t *testing.T) {
	line := realLine(t)
	for _, cut := range []int{30, 60, len(line) / 2} {
		path := writeConf(t, line[:cut]+")")
		if _, err := Read(path); err == nil {
			t.Errorf("cut at %d: want an error", cut)
		}
	}
}

func TestReadRejectsWrongOuterType(t *testing.T) {
	// \0\0\0\x2 + payload: an int, not the expected QVariantMap.
	path := writeConf(t, `LastWritingTool=@Variant(\0\0\0\x2\0\0\0\xf)`)
	if _, err := Read(path); err == nil {
		t.Fatal("want an error for a non-map @Variant")
	}
}

func TestUnescapeINI(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{`abc`, "abc", true},
		{`\0\x61\0`, "\x00a\x00", true},            // hex run ends at the next backslash
		{`\x1c\0L`, "\x1c\x00L", true},             // hex then literal
		{`\\\;\,\"`, `\;,"`, true},                 // punctuation escapes
		{`\a\b\t\n\v\f\r`, "\a\b\t\n\v\f\r", true}, // control escapes
		{`\xff`, "\xff", true},                     // full byte
		{`\x100`, "", false},                       // wider than a byte
		{`\x`, "", false},                          // no digits
		{`\q`, "", false},                          // unknown escape
		{`tail\`, "", false},                       // dangling backslash
	} {
		got, err := unescapeINI(tc.in)
		if tc.ok != (err == nil) {
			t.Errorf("unescapeINI(%q) err = %v, want ok=%v", tc.in, err, tc.ok)
			continue
		}
		if tc.ok && string(got) != tc.want {
			t.Errorf("unescapeINI(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestControlsMapping(t *testing.T) {
	for _, tc := range []struct {
		st               State
		pen, size, color string
	}{
		{State{Pen: 15, PenColor: 0, PenSize: 2}, "pen_ballpoint", "size_medium", "color_black"},
		{State{Pen: 2, PenColor: 7, PenSize: 1}, "pen_ballpoint", "size_thin", "color_red"},
		{State{Pen: 17, PenColor: 6, PenSize: 3}, "pen_fineliner", "size_thick", "color_blue"},
		{State{Pen: 21, PenColor: 1, PenSize: 2.5}, "pen_calligraphy", "size_thick", "color_gray"},
		{State{Pen: 18, PenColor: 3, PenSize: 1}, "pen_highlighter", "size_thin", ""}, // yellow: no calibrated control
		{State{Pen: 15, PenColor: -1, PenSize: 0}, "pen_ballpoint", "", ""},           // size/color keys absent
		{State{Pen: 25, PenColor: 0, PenSize: 2}, "", "", ""},                         // unknown pen: restore nothing
		{State{Pen: -1, PenColor: 0, PenSize: 2}, "", "", ""},                         // pen key absent
	} {
		pen, size, color := tc.st.Controls()
		if pen != tc.pen || size != tc.size || color != tc.color {
			t.Errorf("%+v Controls() = %q %q %q, want %q %q %q",
				tc.st, pen, size, color, tc.pen, tc.size, tc.color)
		}
	}
}
