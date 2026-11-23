package web

import (
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// TestCeremoniesAreBounded: what a page that anybody can load makes the
// server hold has a ceiling, the oldest going first, and what has expired
// goes on the next start whatever the count.
func TestCeremoniesAreBounded(t *testing.T) {
	t.Parallel()
	var c ceremonies
	now := time.Now()
	first := c.start("example.com", &webauthn.SessionData{}, now)
	for i := range ceremoniesMax {
		c.start("example.com", &webauthn.SessionData{}, now.Add(time.Duration(i+1)*time.Millisecond))
	}
	if len(c.m) != ceremoniesMax {
		t.Errorf("held %d ceremonies, want %d", len(c.m), ceremoniesMax)
	}
	if _, ok := c.take(first, "example.com", now); ok {
		t.Error("the oldest ceremony outlived the ceiling")
	}

	c.start("example.com", &webauthn.SessionData{}, now.Add(2*ceremonyTTL))
	if len(c.m) != 1 {
		t.Errorf("held %d ceremonies after they expired, want the new one", len(c.m))
	}
}

func TestACeremonyIsForItsHostAndItsTime(t *testing.T) {
	t.Parallel()
	var c ceremonies
	now := time.Now()
	if _, ok := c.take(c.start("example.com", &webauthn.SessionData{}, now), "elsewhere.example", now); ok {
		t.Error("a ceremony was taken at another host")
	}
	if _, ok := c.take(c.start("example.com", &webauthn.SessionData{}, now), "example.com", now.Add(ceremonyTTL+time.Second)); ok {
		t.Error("an expired ceremony was taken")
	}
}
