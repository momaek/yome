package inject

// PressureEnvelope shapes pen pressure along a stroke. Constant pressure
// injects an even hairline that reads as machine-drawn; ramping in at the
// start and tapering at the end makes xochitl's pressure brushes vary the
// width, which is what sells the strokes as handwriting (M0 finding).
//
// The profile: rise from Attack*Base to full over the first AttackFrac of the
// stroke, hold full until BodyFrac, then fall to (1-Release)*Base at the end.
type PressureEnvelope struct {
	Base        int     // body pressure, 0..WacomMaxPres
	Attack      float64 // pressure at the very start, as a fraction of Base
	AttackFrac  float64 // fraction of the stroke spent rising to full
	BodyFrac    float64 // fraction at which the taper begins
	Release     float64 // how far pressure falls by the stroke's end
	MinLengthPx float64 // strokes shorter than this use constant pressure
}

// DefaultPressure is the envelope calibrated on device in M0.
func DefaultPressure() PressureEnvelope {
	return PressureEnvelope{
		Base:        3400,
		Attack:      0.55,
		AttackFrac:  0.10,
		BodyFrac:    0.70,
		Release:     0.72,
		MinLengthPx: 12,
	}
}

// At returns the pressure at fraction frac (0..1) along a stroke of the given
// total length. Dots and tiny strokes get constant pressure: there is no room
// for an envelope, and tapering them makes them vanish.
func (e PressureEnvelope) At(frac, strokeLenPx float64) int32 {
	if strokeLenPx < e.MinLengthPx {
		return int32(e.Base)
	}
	base := float64(e.Base)
	switch {
	case frac < e.AttackFrac:
		if e.AttackFrac == 0 {
			return int32(base)
		}
		p := base * (e.Attack + (1-e.Attack)*frac/e.AttackFrac)
		return int32(clamp(p, 0, WacomMaxPres))
	case frac < e.BodyFrac:
		return int32(clamp(base, 0, WacomMaxPres))
	default:
		tail := 1 - e.BodyFrac
		if tail <= 0 {
			return int32(clamp(base, 0, WacomMaxPres))
		}
		p := base * (1 - e.Release*(frac-e.BodyFrac)/tail)
		return int32(clamp(p, 0, WacomMaxPres))
	}
}
