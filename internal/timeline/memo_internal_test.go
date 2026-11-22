package timeline

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
)

// counting is a source that says how often it was asked.
type counting struct {
	Source
	months, pages int
}

func (c *counting) Months(ctx context.Context, owner string, kind db.Kind) ([]db.Month, error) {
	c.months++
	return c.Source.Months(ctx, owner, kind)
}

func (c *counting) Timeline(ctx context.Context, owner string, f db.CaptureFilter) ([]db.Capture, error) {
	c.pages++
	return c.Source.Timeline(ctx, owner, f)
}

// fixed is one month of three photographs, two of them sharing a name.
type fixed struct{}

var june = time.Date(2024, 6, 10, 12, 0, 0, 0, time.UTC)

func (fixed) Months(context.Context, string, db.Kind) ([]db.Month, error) {
	return []db.Month{db.MonthOf(june)}, nil
}

func (fixed) Timeline(_ context.Context, _ string, f db.CaptureFilter) ([]db.Capture, error) {
	if !f.After.AtStart() {
		return nil, nil
	}
	return []db.Capture{
		{File: db.File{ID: 3, Path: "a/IMG_1.jpg"}, SortAt: june},
		{File: db.File{ID: 1, Path: "b/IMG_1.jpg"}, SortAt: june},
		{File: db.File{ID: 2, Path: "b/IMG_2.jpg"}, SortAt: june},
	}, nil
}

// TestStepsThroughAMonthReadItOnce is #561: a tree per request read its month
// whole for every step of the viewer; kept, ten steps are one read, a change
// to the index is read again at once, and so is an answer past its seconds.
func TestStepsThroughAMonthReadItOnce(t *testing.T) {
	src := &counting{Source: fixed{}}
	memo := Memoize(src)
	at := june
	memo.clock = func() time.Time { return at }

	for range 10 {
		tree := New(memo, "edu", db.KindImage)
		if _, err := tree.PathOf(t.Context(), db.Capture{File: db.File{ID: 2}, SortAt: june}); err != nil {
			t.Fatal(err)
		}
	}
	if src.pages != 1 {
		t.Errorf("ten steps read the month %d times, want once", src.pages)
	}

	memo.Changed()
	if _, err := New(memo, "edu", db.KindImage).Month(t.Context(), db.MonthOf(june)); err != nil {
		t.Fatal(err)
	}
	if src.pages != 2 {
		t.Errorf("after a change the month was read %d times in all, want twice", src.pages)
	}

	at = at.Add(memoFor + time.Second)
	if _, err := New(memo, "edu", db.KindImage).Month(t.Context(), db.MonthOf(june)); err != nil {
		t.Fatal(err)
	}
	if src.pages != 3 {
		t.Errorf("past its seconds the month was read %d times in all, want three", src.pages)
	}
}

// TestPathOfAgreesWithTheListing: naming a photograph by its id is the name the
// collection lists it under, duplicates included.
func TestPathOfAgreesWithTheListing(t *testing.T) {
	tree := New(fixed{}, "edu", db.KindImage)
	named, err := tree.Month(t.Context(), db.MonthOf(june))
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range named {
		got, err := tree.PathOf(t.Context(), p)
		if err != nil || got != "2024/06/"+name {
			t.Errorf("PathOf(%d) = %q, %v; the listing calls it %q", p.File.ID, got, err, name)
		}
	}
	if _, err := tree.PathOf(t.Context(), db.Capture{File: db.File{ID: 99}, SortAt: june}); err == nil {
		t.Error("a photograph the month does not hold was named")
	}
}

// broken is a source that fails, so nothing is kept from it.
type broken struct{}

var errBroken = errors.New("the index is on fire")

func (broken) Months(context.Context, string, db.Kind) ([]db.Month, error) { return nil, errBroken }
func (broken) Timeline(context.Context, string, db.CaptureFilter) ([]db.Capture, error) {
	return nil, errBroken
}

// TestAFailureIsNotKept, and a memo that fills up is emptied of what is old
// rather than growing.
func TestAFailureIsNotKeptAndTheMemoIsBounded(t *testing.T) {
	memo := Memoize(broken{})
	if _, err := memo.Months(t.Context(), "edu", db.KindImage); !errors.Is(err, errBroken) {
		t.Errorf("Months = %v", err)
	}
	if _, err := memo.Timeline(t.Context(), "edu", db.CaptureFilter{}); !errors.Is(err, errBroken) {
		t.Errorf("Timeline = %v", err)
	}
	if len(memo.kept) != 0 {
		t.Errorf("a failure was kept: %v", memo.kept)
	}

	full := Memoize(fixed{})
	at := june
	full.clock = func() time.Time { return at }
	for i := range memoLimit {
		if _, err := full.Months(t.Context(), strconv.Itoa(i), db.KindImage); err != nil {
			t.Fatal(err)
		}
	}
	at = at.Add(memoFor + time.Second)
	if _, err := full.Months(t.Context(), "one more", db.KindImage); err != nil {
		t.Fatal(err)
	}
	if len(full.kept) != 1 {
		t.Errorf("a full memo holds %d after the old went, want the one just read", len(full.kept))
	}
	for i := range memoLimit + 1 {
		if _, err := full.Months(t.Context(), fmt.Sprint("fresh", i), db.KindImage); err != nil {
			t.Fatal(err)
		}
	}
	if len(full.kept) > memoLimit {
		t.Errorf("the memo grew to %d", len(full.kept))
	}
	var nothing *Memo
	nothing.Changed()
}
