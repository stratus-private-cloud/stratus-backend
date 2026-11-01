package web_test

import (
	htmlstd "html"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"

	"github.com/stratus-private-cloud/stratus-backend/internal/calendar"
	"github.com/stratus-private-cloud/stratus-backend/internal/db"
)

const madridZone = `BEGIN:VTIMEZONE
TZID:Europe/Madrid
BEGIN:STANDARD
DTSTART:19701025T030000
TZOFFSETFROM:+0200
TZOFFSETTO:+0100
RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU
END:STANDARD
BEGIN:DAYLIGHT
DTSTART:19700329T020000
TZOFFSETFROM:+0100
TZOFFSETTO:+0200
RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU
END:DAYLIGHT
END:VTIMEZONE`

// events is every event in an object, the series first if there is one.
func events(t *testing.T, meta db.Store, name string) (series *ical.Component, overrides []*ical.Component, o db.CalendarObject) {
	t.Helper()
	o, err := calendar.New(meta).Object(t.Context(), username, calendar.DefaultName, name)
	if err != nil {
		t.Fatal(err)
	}
	cal, err := calendar.Decode(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cal.Children {
		switch {
		case c.Name != ical.CompEvent:
		case c.Props.Get(ical.PropRecurrenceID) == nil:
			series = c
		default:
			overrides = append(overrides, c)
		}
	}
	return series, overrides, o
}

func local(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC).In(time.Local)
}

// TestOneOccurrenceIsChangedOnItsOwn is #445's first half: the month links
// an occurrence by its RECURRENCE-ID, and saving it writes an override that
// carries what the series does, alarm included, and leaves the series alone.
func TestOneOccurrenceIsChangedOnItsOwn(t *testing.T) {
	t.Parallel()
	h, _, meta := browserOver(t)
	cookie := signIn(t, h)
	putEvent(t, meta, calendar.DefaultName, "standup", "DTSTART:20240104T090000Z", "DTEND:20240104T093000Z",
		"RRULE:FREQ=WEEKLY", "SUMMARY:Standup", "BEGIN:VALARM", "ACTION:DISPLAY", "TRIGGER:-PT10M",
		"DESCRIPTION:Standup", "END:VALARM")
	_, _, was := events(t, meta, "standup.ics")

	link := "/event/edit/default/standup.ics?instance=20240111T090000Z"
	if month := get(t, h, "/calendars/edu/default/?month=2024-01", cookie).Body.String(); !strings.Contains(month, `href="`+link+`"`) {
		t.Fatalf("the month does not link the occurrence:\n%s", month)
	}
	page := get(t, h, link, cookie)
	body := page.Body.String()
	for _, want := range []string{
		"This occurrence", `href="/event/edit/default/standup.ics">Every occurrence`, "changes this one alone",
		`name="start-date" type="date" value="` + local(2024, 1, 11, 9, 0).Format(time.DateOnly) + `"`,
		`href="/event/delete/default/standup.ics?instance=20240111T090000Z"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the occurrence's form has no %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `name="repeat"`) {
		t.Errorf("an occurrence is offered a rule")
	}

	if rec := post(t, h, link, formOf(t, body), cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving untouched = %d:\n%s", rec.Code, rec.Body)
	}
	if _, _, o := events(t, meta, "standup.ics"); o.Data != was.Data {
		t.Errorf("saving an occurrence untouched wrote:\n%s", o.Data)
	}

	moved := formOf(t, body)
	moved.Set("summary", "Standup, late")
	moved.Set("start-time", local(2024, 1, 11, 10, 0).Format("15:04"))
	moved.Set("end-time", local(2024, 1, 11, 10, 30).Format("15:04"))
	rec := post(t, h, link, moved, cookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/calendars/edu/default/?month=2024-01" {
		t.Fatalf("saving the occurrence = %d to %q:\n%s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	series, overrides, o := events(t, meta, "standup.ics")
	if len(overrides) != 1 {
		t.Fatalf("%d overrides:\n%s", len(overrides), o.Data)
	}
	if s, _ := series.Props.Text(ical.PropSummary); s != "Standup" || series.Props.Get(ical.PropRecurrenceRule) == nil {
		t.Errorf("the series was changed:\n%s", o.Data)
	}
	for _, want := range []string{
		"RECURRENCE-ID:20240111T090000Z", "DTSTART:20240111T100000Z", "DTEND:20240111T103000Z",
		`SUMMARY:Standup\, late`, "SEQUENCE:1", "TRIGGER:-PT10M",
	} {
		if !strings.Contains(o.Data, want) {
			t.Errorf("the override has no %s:\n%s", want, o.Data)
		}
	}
	if overrides[0].Props.Get(ical.PropRecurrenceRule) != nil {
		t.Errorf("the override repeats:\n%s", o.Data)
	}

	again := formOf(t, get(t, h, link, cookie).Body.String())
	again.Set("location", "Room 2")
	if rec := post(t, h, link, again, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving the override again = %d:\n%s", rec.Code, rec.Body)
	}
	if _, overrides, o = events(t, meta, "standup.ics"); len(overrides) != 1 || !strings.Contains(o.Data, "LOCATION:Room 2") ||
		!strings.Contains(o.Data, "SEQUENCE:2") {
		t.Errorf("a second save of the occurrence wrote:\n%s", o.Data)
	}

	month := htmlstd.UnescapeString(get(t, h, "/calendars/edu/default/?month=2024-01", cookie).Body.String())
	if !strings.Contains(month, "Standup, late") || strings.Count(month, "?instance=20240111T090000Z") != 2 {
		t.Errorf("the month does not show the moved occurrence once in each view:\n%s", month)
	}
}

// TestAnOccurrenceIsWrittenInItsSeriesZone: an override and an EXDATE name
// the occurrence as the series' DTSTART does, here a time in Madrid, and an
// all-day series by its date.
func TestAnOccurrenceIsWrittenInItsSeriesZone(t *testing.T) {
	t.Parallel()
	h, _, meta := browserOver(t)
	cookie := signIn(t, h)
	putICS(t, meta, calendar.DefaultName, "madrid.ics", "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//phone//EN\n"+madridZone+`
BEGIN:VEVENT
UID:madrid
DTSTAMP:20240101T000000Z
DTSTART;TZID=Europe/Madrid:20240104T090000
DTEND;TZID=Europe/Madrid:20240104T100000
RRULE:FREQ=WEEKLY
SUMMARY:Madrid
END:VEVENT
END:VCALENDAR
`)
	putEvent(t, meta, calendar.DefaultName, "days", "DTSTART;VALUE=DATE:20240306", "DTEND;VALUE=DATE:20240307",
		"RRULE:FREQ=WEEKLY", "SUMMARY:Market")

	link := "/event/edit/default/madrid.ics?instance=20240111T080000Z"
	form := formOf(t, get(t, h, link, cookie).Body.String())
	form.Set("summary", "Madrid, once")
	if rec := post(t, h, link, form, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving = %d:\n%s", rec.Code, rec.Body)
	}
	if rec := post(t, h, "/event/delete/default/madrid.ics?instance=20240118T080000Z", url.Values{}, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("deleting = %d:\n%s", rec.Code, rec.Body)
	}
	_, _, o := events(t, meta, "madrid.ics")
	for _, want := range []string{
		"RECURRENCE-ID;TZID=Europe/Madrid:20240111T090000", "DTSTART;TZID=Europe/Madrid:20240111T090000",
		"DTEND;TZID=Europe/Madrid:20240111T100000", "EXDATE;TZID=Europe/Madrid:20240118T090000",
	} {
		if !strings.Contains(o.Data, want) {
			t.Errorf("the Madrid series has no %s:\n%s", want, o.Data)
		}
	}

	if month := get(t, h, "/calendars/edu/default/?month=2024-03", cookie).Body.String(); !strings.Contains(month, "?instance=20240313\"") {
		t.Fatalf("the month does not link the day's occurrence:\n%s", month)
	}
	if rec := post(t, h, "/event/delete/default/days.ics?instance=20240313", url.Values{}, cookie); rec.Code != http.StatusSeeOther ||
		rec.Header().Get("Location") != "/calendars/edu/default/?month=2024-03" {
		t.Fatalf("deleting a day = %d to %q:\n%s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	if _, _, o = events(t, meta, "days.ics"); !strings.Contains(o.Data, "EXDATE;VALUE=DATE:20240313") {
		t.Errorf("the market has no EXDATE for the 13th:\n%s", o.Data)
	}
	if month := get(t, h, "/calendars/edu/default/?month=2024-03", cookie).Body.String(); strings.Contains(month, "?instance=20240313\"") {
		t.Errorf("the deleted day is still on the month")
	}
}

// TestDeletingOneOccurrence asks which, takes the override with it, and
// deletes an object that held nothing else.
func TestDeletingOneOccurrence(t *testing.T) {
	t.Parallel()
	h, _, meta := browserOver(t)
	cookie := signIn(t, h)
	putEvent(t, meta, calendar.DefaultName, "standup", "DTSTART:20240104T090000Z", "RRULE:FREQ=WEEKLY", "SUMMARY:Standup")
	link := "/event/edit/default/standup.ics?instance=20240111T090000Z"
	form := formOf(t, get(t, h, link, cookie).Body.String())
	form.Set("summary", "Standup, late")
	if rec := post(t, h, link, form, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving = %d:\n%s", rec.Code, rec.Body)
	}

	page := get(t, h, "/event/delete/default/standup.ics?instance=20240111T090000Z", cookie).Body.String()
	for _, want := range []string{
		`action="/event/delete/default/standup.ics?instance=20240111T090000Z"`, "Delete this occurrence",
		`action="/event/delete/default/standup.ics"`, "Delete every occurrence",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page has no %q:\n%s", want, page)
		}
	}
	if rec := post(t, h, "/event/delete/default/standup.ics?instance=20240111T090000Z", url.Values{}, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("deleting = %d:\n%s", rec.Code, rec.Body)
	}
	series, overrides, o := events(t, meta, "standup.ics")
	if len(overrides) != 0 || !strings.Contains(o.Data, "EXDATE:20240111T090000Z") || series.Props.Get(ical.PropSequence) == nil {
		t.Errorf("deleting the occurrence stored:\n%s", o.Data)
	}
	if rec := get(t, h, link, cookie); rec.Code != http.StatusNotFound {
		t.Errorf("the deleted occurrence's form = %d", rec.Code)
	}

	putEvent(t, meta, calendar.DefaultName, "moved", "RECURRENCE-ID:20240104T090000Z", "DTSTART:20240104T100000Z", "SUMMARY:Moved")
	if page := get(t, h, "/event/delete/default/moved.ics?instance=20240104T090000Z", cookie).Body.String(); strings.Contains(page, "Delete every occurrence") {
		t.Errorf("an object with no series offers every occurrence:\n%s", page)
	}
	if rec := post(t, h, "/event/delete/default/moved.ics?instance=20240104T090000Z", url.Values{}, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("deleting the only occurrence = %d:\n%s", rec.Code, rec.Body)
	}
	if _, err := calendar.New(meta).Object(t.Context(), username, calendar.DefaultName, "moved.ics"); err == nil {
		t.Error("an object with no event left was kept")
	}
}

// TestAnOccurrenceThatIsNotOne is refused rather than written.
func TestAnOccurrenceThatIsNotOne(t *testing.T) {
	t.Parallel()
	h, _, meta := browserOver(t)
	cookie := signIn(t, h)
	putEvent(t, meta, calendar.DefaultName, "standup", "DTSTART:20240104T090000Z", "RRULE:FREQ=WEEKLY", "SUMMARY:Standup")
	putEvent(t, meta, calendar.DefaultName, "lunch", "DTSTART:20240104T120000Z", "SUMMARY:Lunch")
	for _, target := range []string{
		"/event/edit/default/standup.ics?instance=20240112T090000Z", "/event/edit/default/standup.ics?instance=soon",
		"/event/edit/default/lunch.ics?instance=20240104T120000Z", "/event/delete/default/standup.ics?instance=20240112T090000Z",
	} {
		if rec := get(t, h, target, cookie); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d", target, rec.Code)
		}
	}
	_, _, was := events(t, meta, "standup.ics")
	if rec := post(t, h, "/event/edit/default/standup.ics?instance=20240112T090000Z", url.Values{"summary": {"x"}}, cookie); rec.Code != http.StatusConflict {
		t.Errorf("saving no occurrence = %d", rec.Code)
	}
	if rec := post(t, h, "/event/delete/default/standup.ics?instance=20240112T090000Z", url.Values{}, cookie); rec.Code != http.StatusNotFound {
		t.Errorf("deleting no occurrence = %d", rec.Code)
	}
	if _, _, o := events(t, meta, "standup.ics"); o.Data != was.Data {
		t.Errorf("an occurrence that is not one wrote:\n%s", o.Data)
	}
}

// TestAnEventIsMadeToRepeat is #445's second half: a rule for a new event,
// for one that did not repeat, and a simple rule changed; one the form
// cannot write is shown and kept.
func TestAnEventIsMadeToRepeat(t *testing.T) {
	t.Parallel()
	h, _, meta := browserOver(t)
	cookie := signIn(t, h)

	if page := get(t, h, "/event/new/default", cookie).Body.String(); !strings.Contains(page, `<option value="" selected>Does not repeat</option>`) {
		t.Errorf("a new event is not offered a rule:\n%s", page)
	}
	rec := post(t, h, "/event/new/default", url.Values{
		"summary": {"Gym"}, "start-date": {"2024-03-05"}, "start-time": {"18:00"}, "repeat": {"weekly"}, "until": {"2024-03-26"},
	}, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("saving = %d:\n%s", rec.Code, rec.Body)
	}
	objects, _ := calendar.New(meta).Objects(t.Context(), username, calendar.DefaultName)
	until := time.Date(2024, 3, 26, 23, 59, 59, 0, time.Local).UTC().Format("20060102T150405Z")
	if !strings.Contains(objects[0].Data, "RRULE:FREQ=WEEKLY;UNTIL="+until) {
		t.Errorf("the gym repeats as:\n%s", objects[0].Data)
	}
	if month := get(t, h, "/calendars/edu/default/?month=2024-03", cookie).Body.String(); strings.Count(month, "?instance=") != 2*4 {
		t.Errorf("the month has %d occurrence links, want four in each view", strings.Count(month, "?instance="))
	}

	rec = post(t, h, "/event/new/default", url.Values{
		"summary": {"Rent"}, "allday": {"1"}, "start-date": {"2024-03-01"}, "repeat": {"monthly"}, "until": {"2024-12-01"},
	}, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("saving an all-day rule = %d:\n%s", rec.Code, rec.Body)
	}
	objects, _ = calendar.New(meta).Objects(t.Context(), username, calendar.DefaultName)
	found := false
	for _, o := range objects {
		found = found || strings.Contains(o.Data, "RRULE:FREQ=MONTHLY;UNTIL=20241201")
	}
	if !found {
		t.Errorf("the rent does not repeat by the date")
	}

	putEvent(t, meta, calendar.DefaultName, "lunch", "DTSTART:20240104T120000Z", "SUMMARY:Lunch")
	form := formOf(t, get(t, h, "/event/edit/default/lunch.ics", cookie).Body.String())
	form.Set("repeat", "daily")
	if rec := post(t, h, "/event/edit/default/lunch.ics", form, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("making lunch repeat = %d:\n%s", rec.Code, rec.Body)
	}
	if _, o := storedEvent(t, meta, "lunch.ics"); !strings.Contains(o.Data, "RRULE:FREQ=DAILY\r\n") || !strings.Contains(o.Data, "SEQUENCE:1") {
		t.Errorf("lunch repeats as:\n%s", o.Data)
	}

	page := get(t, h, "/event/edit/default/lunch.ics", cookie).Body.String()
	if !strings.Contains(page, `<option value="daily" selected>`) || strings.Contains(page, "Does not repeat") {
		t.Errorf("a simple rule is not offered to change, or is offered to stop:\n%s", page)
	}
	form = formOf(t, page)
	form.Set("repeat", "")
	if rec := post(t, h, "/event/edit/default/lunch.ics", form, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("posting no rule = %d", rec.Code)
	}
	if _, o := storedEvent(t, meta, "lunch.ics"); !strings.Contains(o.Data, "RRULE:FREQ=DAILY") {
		t.Errorf("a rule was taken away:\n%s", o.Data)
	}
	form.Set("repeat", "monthly")
	if rec := post(t, h, "/event/edit/default/lunch.ics", form, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("changing the rule = %d:\n%s", rec.Code, rec.Body)
	}
	if _, o := storedEvent(t, meta, "lunch.ics"); !strings.Contains(o.Data, "RRULE:FREQ=MONTHLY") {
		t.Errorf("lunch repeats as:\n%s", o.Data)
	}

	putEvent(t, meta, calendar.DefaultName, "class", "DTSTART:20240104T120000Z", "RRULE:FREQ=WEEKLY;BYDAY=TH,TU", "SUMMARY:Class")
	page = get(t, h, "/event/edit/default/class.ics", cookie).Body.String()
	if strings.Contains(page, `name="repeat"`) || !strings.Contains(page, "Repeats weekly") {
		t.Errorf("a rule the form cannot write is offered to change:\n%s", page)
	}
	form = formOf(t, page)
	form.Set("summary", "Yoga")
	form.Set("repeat", "daily")
	if rec := post(t, h, "/event/edit/default/class.ics", form, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving = %d", rec.Code)
	}
	if _, o := storedEvent(t, meta, "class.ics"); !strings.Contains(o.Data, "RRULE:FREQ=WEEKLY;BYDAY=TH,TU") {
		t.Errorf("a rule the form cannot write was replaced:\n%s", o.Data)
	}
}

// TestASeriesWithExceptionsKeepsItsStartAndRule: moving it or changing how
// it repeats would leave its overrides and EXDATEs naming nothing.
func TestASeriesWithExceptionsKeepsItsStartAndRule(t *testing.T) {
	t.Parallel()
	h, _, meta := browserOver(t)
	cookie := signIn(t, h)
	putEvent(t, meta, calendar.DefaultName, "standup", "DTSTART:20240104T090000Z", "DTEND:20240104T093000Z",
		"RRULE:FREQ=WEEKLY", "EXDATE:20240111T090000Z", "SUMMARY:Standup")
	page := get(t, h, "/event/edit/default/standup.ics", cookie).Body.String()

	for field, value := range map[string]string{
		"start-time": local(2024, 1, 4, 10, 0).Format("15:04"), "start-date": "2024-01-05", "repeat": "daily", "until": "2024-06-01",
	} {
		form := formOf(t, page)
		form.Set(field, value)
		rec := post(t, h, "/event/edit/default/standup.ics", form, cookie)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "changed or deleted on their own") {
			t.Errorf("changing %s = %d:\n%s", field, rec.Code, rec.Body)
		}
	}

	form := formOf(t, page)
	form.Set("summary", "Daily standup")
	form.Set("end-time", local(2024, 1, 4, 9, 45).Format("15:04"))
	if rec := post(t, h, "/event/edit/default/standup.ics", form, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("changing the title and the end = %d:\n%s", rec.Code, rec.Body)
	}
	if _, o := storedEvent(t, meta, "standup.ics"); !strings.Contains(o.Data, "DTEND:20240104T094500Z") || !strings.Contains(o.Data, "EXDATE:20240111T090000Z") {
		t.Errorf("the series stored:\n%s", o.Data)
	}
}

func TestARuleWithAMistakeSavesNothing(t *testing.T) {
	t.Parallel()
	h, _, meta := browserOver(t)
	cookie := signIn(t, h)
	for _, c := range []struct {
		form url.Values
		want string
	}{
		{url.Values{"repeat": {"hourly"}}, "That is not a way to repeat."},
		{url.Values{"repeat": {"weekly"}, "until": {"2024-03-04"}}, "cannot stop repeating before it starts"},
		{url.Values{"repeat": {"weekly"}, "until": {"later"}}, "The last day it repeats on is not a date."},
	} {
		c.form.Set("summary", "x")
		c.form.Set("start-date", "2024-03-05")
		c.form.Set("start-time", "09:00")
		rec := post(t, h, "/event/new/default", c.form, cookie)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%v = %d, want 400 saying %q:\n%s", c.form, rec.Code, c.want, rec.Body)
		}
	}
	if objects, _ := calendar.New(meta).Objects(t.Context(), username, calendar.DefaultName); len(objects) != 0 {
		t.Errorf("a rule with a mistake saved %d objects", len(objects))
	}
}
