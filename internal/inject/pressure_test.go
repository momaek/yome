package inject

import "testing"

func TestEnvelopeShape(t *testing.T) {
	e := DefaultPressure()
	const long = 500.0

	start := e.At(0, long)
	quarter := e.At(0.25, long)
	mid := e.At(0.5, long)
	end := e.At(1.0, long)

	// Attack: the stroke starts light and reaches full by AttackFrac.
	if want := int32(float64(e.Base) * e.Attack); start != want {
		t.Errorf("pressure at the start = %d, want %d", start, want)
	}
	if e.At(e.AttackFrac, long) != int32(e.Base) {
		t.Errorf("pressure at the end of the attack = %d, want the full %d", e.At(e.AttackFrac, long), e.Base)
	}
	// Body: flat and full.
	if quarter != int32(e.Base) || mid != int32(e.Base) {
		t.Errorf("body pressure = %d/%d, want a flat %d", quarter, mid, e.Base)
	}
	// Release: tapering off, which is what gives the stroke its handwritten
	// exit rather than a blunt machine-drawn end.
	if want := int32(float64(e.Base) * (1 - e.Release)); end != want {
		t.Errorf("pressure at the end = %d, want %d", end, want)
	}
	if end >= mid {
		t.Errorf("no taper: end %d is not below mid %d", end, mid)
	}
}

func TestEnvelopeIsMonotonic(t *testing.T) {
	e := DefaultPressure()
	const long = 500.0

	// Rising through the attack.
	prev := e.At(0, long)
	for f := 0.0; f <= e.AttackFrac; f += 0.01 {
		p := e.At(f, long)
		if p < prev {
			t.Fatalf("attack dips at frac %.2f: %d after %d", f, p, prev)
		}
		prev = p
	}
	// Falling through the release.
	prev = e.At(e.BodyFrac, long)
	for f := e.BodyFrac; f <= 1.0; f += 0.01 {
		p := e.At(f, long)
		if p > prev {
			t.Fatalf("release rises at frac %.2f: %d after %d", f, p, prev)
		}
		prev = p
	}
}

func TestShortStrokesKeepConstantPressure(t *testing.T) {
	e := DefaultPressure()
	// A dot has no room for an envelope, and tapering it would erase it.
	short := e.MinLengthPx / 2
	for _, f := range []float64{0, 0.5, 1} {
		if got := e.At(f, short); got != int32(e.Base) {
			t.Errorf("short stroke at frac %.1f = %d, want the constant %d", f, got, e.Base)
		}
	}
}

func TestEnvelopeStaysInDeviceRange(t *testing.T) {
	// A config could ask for more than the digitizer accepts; clamping here
	// beats the driver rejecting or wrapping the value.
	e := PressureEnvelope{Base: WacomMaxPres, Attack: 1.5, AttackFrac: 0.1, BodyFrac: 0.7, Release: 2, MinLengthPx: 12}
	for f := 0.0; f <= 1.0; f += 0.05 {
		p := e.At(f, 500)
		if p < 0 || p > WacomMaxPres {
			t.Errorf("pressure at frac %.2f = %d, outside 0..%d", f, p, WacomMaxPres)
		}
	}
}

func TestEnvelopeHandlesDegenerateConfig(t *testing.T) {
	// Zero-width attack and release phases must not divide by zero.
	e := PressureEnvelope{Base: 3000, Attack: 0.5, AttackFrac: 0, BodyFrac: 1, Release: 0.5, MinLengthPx: 1}
	for _, f := range []float64{0, 0.5, 1} {
		if got := e.At(f, 500); got != 3000 {
			t.Errorf("At(%.1f) = %d, want the base 3000", f, got)
		}
	}
}
