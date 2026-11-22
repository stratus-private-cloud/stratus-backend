package db_test

import (
	"slices"
	"testing"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
)

// TestNamesIgnoreAccents is #592: an accented initial sorted after Z.
func TestNamesIgnoreAccents(t *testing.T) {
	t.Parallel()
	got := []string{"Zoe", "Óscar", "beatriz", "Álvaro", "Alba", "ana"}
	slices.SortFunc(got, db.CompareNames)
	want := []string{"Alba", "Álvaro", "ana", "beatriz", "Óscar", "Zoe"}
	if !slices.Equal(got, want) {
		t.Errorf("sorted = %q, want %q", got, want)
	}
	if c := db.CompareNames("Ana", "Aná"); c == 0 {
		t.Error("two different names compare equal, so the order is not total")
	}
	if f := db.FoldName("Ñandú Élan"); f != "nandu elan" {
		t.Errorf("FoldName = %q", f)
	}
}
