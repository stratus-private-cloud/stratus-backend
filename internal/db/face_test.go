package db_test

import (
	"testing"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
)

func TestPeopleAreNamedFirstThenByHowManyFaces(t *testing.T) {
	t.Parallel()
	got := db.SortPeople([]db.Person{
		{ID: 1, Faces: 3}, {ID: 2, Name: "bea", Faces: 1}, {ID: 3, Faces: 9},
		{ID: 4, Name: "Ana", Faces: 2}, {ID: 5, Faces: 3}, {ID: 6, Name: "Ana", Faces: 5},
	})
	want := []int64{6, 4, 2, 3, 1, 5}
	for i, p := range got {
		if p.ID != want[i] {
			t.Fatalf("SortPeople = %+v, want ids %v", got, want)
		}
	}
	if w := (db.FaceCounts{Files: 10, Scanned: 6, Refused: 1}).Waiting(); w != 3 {
		t.Errorf("Waiting = %d, want 3", w)
	}
}
