package db_test

import (
	"math"
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
)

// TestStorable is #559's half that needs no engine: what it clamps, what it
// cleans, and what it leaves alone.
func TestStorable(t *testing.T) {
	t.Parallel()
	when := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	m := db.Media{
		Width:      math.MaxUint32,
		Height:     -math.MaxUint32,
		DurationMS: math.MaxInt64,
		TakenAt:    when,
		Camera:     "Can\xffon\x00 EOS",
		Title:      "fine",
	}.Storable()

	if m.Width != math.MaxInt32 || m.Height != math.MinInt32 {
		t.Errorf("Width, Height = %d, %d; want the ends of an int4", m.Width, m.Height)
	}
	if m.DurationMS != math.MaxInt64 {
		t.Errorf("DurationMS = %d; an int64 column is left alone", m.DurationMS)
	}
	if !m.TakenAt.Equal(when) {
		t.Errorf("TakenAt = %v; a time is not walked into", m.TakenAt)
	}
	if m.Camera != "Can�on EOS" || m.Title != "fine" {
		t.Errorf("Camera, Title = %q, %q", m.Camera, m.Title)
	}
}
