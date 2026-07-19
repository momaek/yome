package trigger

import (
	"testing"
	"time"
)

// Mask must drop recognition for exactly its window plus the reader-lag
// linger — and no longer, or the user's cancel gesture would be swallowed.
func TestListenerMaskWindow(t *testing.T) {
	l := &Listener{det: NewDetector(testOptions())}

	now := time.Now()
	if l.masked(now) {
		t.Fatal("a fresh listener must not be masked")
	}

	l.Mask()
	if !l.masked(now) {
		t.Error("masked() = false inside an injection window")
	}

	l.Unmask()
	if !l.masked(time.Now()) {
		t.Error("masked() = false right after Unmask, want the linger to still hold")
	}
	if l.masked(time.Now().Add(maskLinger + time.Millisecond)) {
		t.Error("masked() = true after the linger passed, would swallow user gestures")
	}
}

// Nested windows: recognition stays off until the last Unmask.
func TestListenerMaskNests(t *testing.T) {
	l := &Listener{det: NewDetector(testOptions())}
	l.Mask()
	l.Mask()
	l.Unmask()
	if !l.masked(time.Now().Add(maskLinger + time.Millisecond)) {
		t.Error("one Unmask closed two Mask windows")
	}
	l.Unmask()
	if l.masked(time.Now().Add(maskLinger + time.Millisecond)) {
		t.Error("still masked after both windows closed and the linger passed")
	}
}
