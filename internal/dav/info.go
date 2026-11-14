package dav

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"strconv"

	xnet "golang.org/x/net/webdav"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
)

// What the indexer read out of a photograph or a recording, as properties
// in this server's namespace (#502): the half of the web's Details that a
// WebDAV client has no other way to ask for. One property per fact, so a
// client asks for what it draws, and absent -- a 404 in the propstat, which
// is how WebDAV says "none" -- where the file does not carry it.
//
// **Only when asked for by name.** x/net merges whatever DeadProps answers
// into an allprop, so a property computed for every file would be a query
// per row in every listing Finder makes, to hand it facts it throws away.
// A request naming one of these is somebody who wants them, and it is
// almost always a single file.
var (
	takenName    = xml.Name{Space: stratusNamespace, Local: "taken"}
	widthName    = xml.Name{Space: stratusNamespace, Local: "width"}
	heightName   = xml.Name{Space: stratusNamespace, Local: "height"}
	cameraName   = xml.Name{Space: stratusNamespace, Local: "camera"}
	locationName = xml.Name{Space: stratusNamespace, Local: "location"}
	durationName = xml.Name{Space: stratusNamespace, Local: "duration"}
)

var infoNames = map[xml.Name]bool{
	takenName: true, widthName: true, heightName: true,
	cameraName: true, locationName: true, durationName: true,
}

// asksForInfo reads whether a PROPFIND names any of them, and puts the body
// back for x/net to read. Past the limit it says no: a request body that size
// is not a list of properties somebody wrote by hand, and the walk it asks
// for is answered as it was before these existed.
func asksForInfo(r *http.Request) bool {
	if r.Body == nil || r.Body == http.NoBody {
		return false
	}
	head, err := io.ReadAll(io.LimitReader(r.Body, infoPeek))
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), r.Body), r.Body}
	if err != nil {
		return false
	}
	dec := xml.NewDecoder(bytes.NewReader(head))
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		if start, ok := tok.(xml.StartElement); ok && infoNames[start.Name] {
			return true
		}
	}
}

// infoPeek is how much of a PROPFIND body is read to find out.
const infoPeek = 64 << 10

// infoProps are the properties one file's metadata answers. A file the
// indexer has not read, or could not, answers none.
//
// The time is written as the web writes it, with no zone: a camera records
// the wall clock and nothing else, and adding one would be inventing it.
func infoProps(m db.Media) map[xml.Name]xnet.Property {
	props := map[xml.Name]xnet.Property{}
	if !m.Indexed() {
		return props
	}
	if !m.TakenAt.IsZero() {
		props[takenName] = property(takenName, m.TakenAt.Format("2006-01-02T15:04:05"))
	}
	if m.Width > 0 && m.Height > 0 {
		props[widthName] = property(widthName, strconv.Itoa(m.Width))
		props[heightName] = property(heightName, strconv.Itoa(m.Height))
	}
	if m.Camera != "" {
		var camera bytes.Buffer
		_ = xml.EscapeText(&camera, []byte(m.Camera))
		props[cameraName] = property(cameraName, camera.String())
	}
	if m.GPS != nil {
		props[locationName] = property(locationName,
			strconv.FormatFloat(m.GPS.Latitude, 'f', 5, 64)+" "+strconv.FormatFloat(m.GPS.Longitude, 'f', 5, 64))
	}
	if m.DurationMS > 0 {
		props[durationName] = property(durationName, strconv.FormatInt(m.DurationMS, 10))
	}
	return props
}
