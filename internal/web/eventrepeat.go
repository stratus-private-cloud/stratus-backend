package web

import (
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/stratus-private-cloud/stratus-backend/internal/calendar"
	"github.com/stratus-private-cloud/stratus-backend/internal/db"
)

// One occurrence of a repeating event, and a rule for one that does not
// repeat (#445).
//
// **An occurrence is named by its RECURRENCE-ID**, the value Expand wrote for
// it, so the month links it as `?instance=` on the event's own form. Changing
// it writes an override -- the one a client already made, or a copy of the
// series cut down to that occurrence -- and deleting it is an EXDATE on the
// series. Both are written in the form the series' DTSTART is written in, a
// date, a time in its zone, UTC or floating, because that is how a client
// matches an override to its occurrence and one in another form is an event
// of its own.
//
// **A rule is FREQ and at most UNTIL**, which is what the form can say; a
// rule with anything else in it is shown in words and left alone. Stopping a
// series from repeating is not offered.
//
// **A series with exceptions keeps its start and its rule.** An override and
// an EXDATE name an occurrence by when it was, so moving the series or
// changing how it repeats leaves them naming nothing: a moved occurrence then
// shows twice and a deleted one comes back. Refused here as switching between
// all day and timed already is, and a client that knows how to carry them
// along can still do it.
const instanceParam = "instance"

// freqs are the rules the form writes, as its select posts them.
var freqs = map[string]bool{"daily": true, "weekly": true, "monthly": true, "yearly": true}

// instanceTime is the occurrence an ?instance= names, read as Expand compares
// them, in UTC.
func instanceTime(v string) (time.Time, error) {
	p := ical.NewProp(ical.PropRecurrenceID)
	p.Value = v
	if len(v) == len("20060102") {
		p.SetValueType(ical.ValueDate)
	}
	return p.DateTime(time.UTC)
}

// instanceDay is the occurrence as the page's zone sees it: a date is that
// day wherever it is read.
func instanceDay(v string, at time.Time, zone *time.Location) time.Time {
	if len(v) == len("20060102") {
		return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, zone)
	}
	return at.In(zone)
}

// seriesOf is the event a repeating one's occurrences come from, and nil for
// an object that holds only occurrences moved out of a series.
func seriesOf(cal *ical.Calendar) *ical.Component {
	for _, c := range cal.Children {
		if c.Name == ical.CompEvent && c.Props.Get(ical.PropRecurrenceID) == nil {
			return c
		}
	}
	return nil
}

// overrideAt is the occurrence at at as a client moved or changed it, or nil.
func overrideAt(cal *ical.Calendar, at time.Time) *ical.Component {
	for _, c := range cal.Children {
		if c.Name != ical.CompEvent {
			continue
		}
		if p := c.Props.Get(ical.PropRecurrenceID); p != nil {
			if t, err := p.DateTime(time.UTC); err == nil && t.Truncate(time.Second).Equal(at) {
				return c
			}
		}
	}
	return nil
}

// hasExceptions is a series some of whose occurrences were moved, changed or
// deleted on their own.
func hasExceptions(cal *ical.Calendar) bool {
	for _, c := range cal.Children {
		if c.Name != ical.CompEvent {
			continue
		}
		if c.Props.Get(ical.PropRecurrenceID) != nil || c.Props.Get(ical.PropExceptionDates) != nil {
			return true
		}
	}
	return false
}

// target is what a form edits: the event, or the occurrence instance names.
// fresh is an occurrence nobody changed yet, cut from the series and not in
// cal until it is written.
func target(cal *ical.Calendar, instance string) (comp *ical.Component, fresh bool, err error) {
	if instance == "" {
		if comp = master(cal); comp == nil {
			return nil, false, db.ErrNotFound
		}
		return comp, false, nil
	}
	at, err := instanceTime(instance)
	if err != nil {
		return nil, false, db.ErrNotFound
	}
	if comp = overrideAt(cal, at); comp != nil {
		return comp, false, nil
	}
	series := seriesOf(cal)
	if series == nil {
		return nil, false, db.ErrNotFound
	}
	ok, err := calendar.Occurs(series, at)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, db.ErrNotFound
	}
	return cutOccurrence(series, at), true, nil
}

// cutOccurrence is the occurrence of series at at as an override of its own:
// everything the series says, alarms included, without the rule, moved to at.
func cutOccurrence(series *ical.Component, at time.Time) *ical.Component {
	comp := copyComponent(series)
	for _, name := range []string{ical.PropRecurrenceRule, ical.PropRecurrenceDates, ical.PropExceptionDates} {
		comp.Props.Del(name)
	}
	start := series.Props.Get(ical.PropDateTimeStart)
	comp.Props.Set(inFormOf(start, ical.PropRecurrenceID, at))
	comp.Props.Set(inFormOf(start, ical.PropDateTimeStart, at))
	if end := series.Props.Get(ical.PropDateTimeEnd); end != nil {
		from, startErr := start.DateTime(time.UTC)
		to, endErr := end.DateTime(time.UTC)
		if startErr == nil && endErr == nil {
			comp.Props.Set(inFormOf(end, ical.PropDateTimeEnd, at.Add(to.Sub(from))))
		}
	}
	return comp
}

// inFormOf is t as a property called name, written the way ref is: a date,
// a time in ref's zone, UTC, or a floating time.
func inFormOf(ref *ical.Prop, name string, t time.Time) *ical.Prop {
	p := ical.NewProp(name)
	switch {
	case ref.ValueType() == ical.ValueDate:
		p.SetDate(t)
	case ref.Params.Get(ical.PropTimezoneID) != "":
		loc, err := time.LoadLocation(ref.Params.Get(ical.PropTimezoneID))
		if err != nil {
			loc = time.UTC
		}
		p.SetDateTime(t.In(loc))
	case strings.HasSuffix(ref.Value, "Z"):
		p.SetDateTime(t.UTC())
	default:
		p.Value = t.UTC().Format("20060102T150405")
	}
	return p
}

func copyComponent(c *ical.Component) *ical.Component {
	out := &ical.Component{Name: c.Name, Props: ical.Props{}}
	for name, props := range c.Props {
		for _, p := range props {
			params := ical.Params{}
			for k, v := range p.Params {
				params[k] = append([]string(nil), v...)
			}
			p.Params = params
			out.Props[name] = append(out.Props[name], p)
		}
	}
	for _, child := range c.Children {
		out.Children = append(out.Children, copyComponent(child))
	}
	return out
}

// simpleRule is ev's rule as the form writes one: how often, and the last
// day it may fall on in zone. ok is false for a rule the form cannot say,
// and for an occurrence, which has none to change.
func simpleRule(ev *ical.Component, zone *time.Location) (freq, until string, ok bool) {
	if ev.Props.Get(ical.PropRecurrenceID) != nil || ev.Props.Get(ical.PropRecurrenceDates) != nil {
		return "", "", false
	}
	rule := ev.Props.Get(ical.PropRecurrenceRule)
	if rule == nil {
		return "", "", true
	}
	for _, part := range strings.Split(rule.Value, ";") {
		key, value, _ := strings.Cut(part, "=")
		switch strings.ToUpper(key) {
		case "FREQ":
			freq = strings.ToLower(value)
		case "UNTIL":
			p := ical.NewProp("UNTIL")
			p.Value = value
			t, err := p.DateTime(zone)
			if err != nil {
				return "", "", false
			}
			until = t.In(zone).Format(time.DateOnly)
		case "INTERVAL":
			if value != "1" {
				return "", "", false
			}
		case "WKST":
		default:
			return "", "", false
		}
	}
	return freq, until, freqs[freq]
}

// ruleValue is the RRULE for freq until the end of the day until in zone,
// with UNTIL in the form RFC 5545 requires beside start: a date, a floating
// time, or UTC.
func ruleValue(start *ical.Prop, freq, until string, zone *time.Location) string {
	v := "FREQ=" + strings.ToUpper(freq)
	day, err := time.ParseInLocation(time.DateOnly, until, zone)
	if until == "" || err != nil {
		return v
	}
	switch {
	case start.ValueType() == ical.ValueDate:
		return v + ";UNTIL=" + day.Format("20060102")
	case start.Params.Get(ical.PropTimezoneID) == "" && !strings.HasSuffix(start.Value, "Z"):
		return v + ";UNTIL=" + day.Format("20060102") + "T235959"
	default:
		last := time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, 0, zone)
		return v + ";UNTIL=" + last.UTC().Format("20060102T150405Z")
	}
}
