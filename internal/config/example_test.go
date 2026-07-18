package config

import (
	"reflect"
	"testing"
)

// The example file documents the defaults, so it must both parse and agree
// with them. A drifting example teaches users the wrong values.
func TestExampleConfigMatchesTheDefaults(t *testing.T) {
	got, err := Load("../../deploy/config.example.toml")
	if err != nil {
		t.Fatalf("the shipped example config does not load: %v", err)
	}

	want := Default()
	// The example ships without a key, and comments out the fields that have
	// no sensible default.
	want.API.APIKey = ""

	if !reflect.DeepEqual(got, want) {
		t.Errorf("the example config has drifted from the compiled defaults:\n got %+v\nwant %+v", got, want)
	}
}
