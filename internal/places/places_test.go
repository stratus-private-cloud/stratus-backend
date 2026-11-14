package places_test

import (
	"testing"

	"github.com/stratus-private-cloud/stratus-backend/internal/places"
)

func TestAPositionIsNamedAfterItsNearestTown(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		lat, lon float64
		want     string
	}{
		{41.38506, 2.17340, "Barcelona, Spain"},   // the Plaça de Catalunya
		{-18.1416, 178.4419, "Suva, Fiji"},        // a degree from the antimeridian
		{64.1466, -21.9426, "Reykjavík, Iceland"}, // far enough north for the cells to widen
	} {
		got, ok := places.Nearest(tt.lat, tt.lon)
		if !ok || got.String() != tt.want {
			t.Errorf("Nearest(%v, %v) = %q, %v; want %q", tt.lat, tt.lon, got, ok, tt.want)
		}
	}
}

// TestTheMiddleOfNowhereIsNowhere: the nearest town to a point in the
// Atlantic is not where anybody was, and the poles are not a reason to panic.
func TestTheMiddleOfNowhereIsNowhere(t *testing.T) {
	t.Parallel()
	for _, at := range [][2]float64{{30, -40}, {90, 0}, {-90, 180}} {
		if got, ok := places.Nearest(at[0], at[1]); ok {
			t.Errorf("Nearest(%v, %v) = %q, want nothing", at[0], at[1], got)
		}
	}
}
