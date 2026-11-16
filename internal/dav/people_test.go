package dav_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stratus-private-cloud/stratus-backend/internal/dav"
	"github.com/stratus-private-cloud/stratus-backend/internal/db"
)

// peopleSource is Ana, in two photographs.
type peopleSource struct{}

func (peopleSource) People(context.Context) ([]db.Person, error) {
	return []db.Person{{ID: 1, Name: "Ana", Faces: 2}}, nil
}

func (peopleSource) PersonFaces(context.Context, int64, db.FaceCursor, int) ([]db.Face, error) {
	return []db.Face{{ID: 1, FileID: 10, Person: 1}, {ID: 2, FileID: 11, Person: 1}}, nil
}

func (peopleSource) FilesByID(context.Context, string, []int64) ([]db.File, error) {
	return []db.File{
		{ID: 10, Path: "2024/beach.jpg", Size: 3, MIMEType: "image/jpeg", ETag: `"a"`},
		{ID: 11, Path: "2025/party.jpg", Size: 5, MIMEType: "image/jpeg", ETag: `"b"`},
	}, nil
}

// TestThePeopleAreACollectionOfTheirPhotographs is #519's mount: a folder per
// person, the photographs they are in inside it, empty while faces are off,
// and nothing written.
func TestThePeopleAreACollectionOfTheirPhotographs(t *testing.T) {
	t.Parallel()
	on := true
	h := withUser(dav.People("/people/", "/files/", peopleSource{}, func() bool { return on }), "edu")

	wantHrefs(t, hrefs(t, do(t, h, "PROPFIND", "/people/", "", "Depth", "1")), "/people/", "/people/Ana/")
	wantHrefs(t, hrefs(t, do(t, h, "PROPFIND", "/people/Ana/", "", "Depth", "1")),
		"/people/Ana/", "/people/Ana/beach.jpg", "/people/Ana/party.jpg")
	props := propsOf(t, do(t, h, "PROPFIND", "/people/Ana/", "", "Depth", "1").Body.String())
	if got := props["/people/Ana/party.jpg"]["getcontentlength"]; got != "5" {
		t.Errorf("getcontentlength = %q, want 5", got)
	}
	if rec := do(t, h, http.MethodPut, "/people/Ana/new.jpg", "x"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT = %d, want 405", rec.Code)
	}

	on = false
	wantHrefs(t, hrefs(t, do(t, h, "PROPFIND", "/people/", "", "Depth", "1")), "/people/")
	if rec := do(t, h, "PROPFIND", "/people/Ana/", "", "Depth", "1"); rec.Code != http.StatusNotFound {
		t.Errorf("Ana with faces off = %d, want 404", rec.Code)
	}
}
