package client

import (
	"testing"
)

func TestDetectCountryCode(t *testing.T) {
	cc := CountryCode()
	if cc == "" {
		t.Skip("could not detect country code from timezone")
	}
	if len(cc) != 2 {
		t.Errorf("expected 2-char country code, got %q", cc)
	}
}

func TestTzCountryMap(t *testing.T) {
	tests := map[string]string{
		"America/New_York":    "us",
		"America/Los_Angeles": "us",
		"Europe/London":       "gb",
		"Asia/Tokyo":          "jp",
		"Australia/Sydney":    "au",
	}
	for tz, want := range tests {
		got, ok := tzCountryMap[tz]
		if !ok {
			t.Errorf("timezone %s not in map", tz)
			continue
		}
		if got != want {
			t.Errorf("tzCountryMap[%s] = %s, want %s", tz, got, want)
		}
	}
}
