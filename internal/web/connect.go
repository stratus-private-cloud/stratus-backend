package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	texttemplate "text/template"
)

// The page that sets a phone up for the calendars and the contacts (#485):
// a configuration profile for iOS and macOS, and a link that opens DAVx5 on
// Android. Neither is a client of our own -- the profile is read by the
// system's own accounts and the link by an app that already exists -- so this
// is principle 2 made shorter to follow rather than bent.
const (
	connectPath = "/connect"
	profilePath = connectPath + "/stratus.mobileconfig"
)

// connectView is the page: where the server is, as each client needs it said.
type connectView struct {
	// Server is the origin, and User who signs in to it, for the clients
	// neither door reaches and somebody types into.
	Server  string
	User    string
	Profile string
	// DAVx5 is a template.URL because html/template rewrites any scheme it does
	// not know to #ZgotmplZ, and davx5:// is the one this link exists for.
	DAVx5     template.URL
	Calendars bool
	Contacts  bool
}

// origin is where a client reaches this server: whether it is TLS, the host
// and the port, with the port said even when it is the default because the
// profile has a field for it.
type origin struct {
	TLS  bool
	Host string
	Port int
}

// originOf believes the request about where it was sent, for the reason
// absolute gives: a wrong answer is shown to the owner, who can see it.
func originOf(r *http.Request) origin {
	o := origin{TLS: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", Host: r.Host}
	if host, port, err := net.SplitHostPort(r.Host); err == nil {
		if p, perr := strconv.Atoi(port); perr == nil {
			o.Host, o.Port = host, p
		}
	}
	if o.Port == 0 {
		o.Port = 80
		if o.TLS {
			o.Port = 443
		}
	}
	return o
}

// davx5Link opens DAVx5's login on this server with the user filled in. It
// maps davx5:// to https and caldav:// to http, and finds both services from
// the root through /.well-known. No password in it: it would sit in the page.
func davx5Link(o origin, user string) string {
	u := url.URL{Scheme: "caldav", User: url.User(user), Host: o.hostPort(), Path: "/"}
	if o.TLS {
		u.Scheme = "davx5"
	}
	return u.String()
}

func (o origin) hostPort() string {
	if (o.TLS && o.Port == 443) || (!o.TLS && o.Port == 80) {
		return o.Host
	}
	return net.JoinHostPort(o.Host, strconv.Itoa(o.Port))
}

func (h *handler) connect(w http.ResponseWriter, r *http.Request, user string) {
	o := originOf(r)
	h.render(w, http.StatusOK, pageConnect, view{
		Title: "Connect a device", User: user,
		Connect: &connectView{
			Server:    absolute(r, "/"),
			User:      user,
			Profile:   profilePath,
			DAVx5:     template.URL(davx5Link(o, user)), //nolint:gosec // built by url.URL from the request's host
			Calendars: h.calendars != nil,
			Contacts:  h.contacts != nil,
		},
	})
}

// profile is Apple's configuration profile with an account for each of
// CalDAV and CardDAV. Unsigned, so iOS calls it unverified; signing it would
// need a certificate an install does not have. No password in it either: iOS
// asks for one as it installs, and a file that held it would be a credential
// left in somebody's Downloads.
//
// The identifiers are the same for the same server and user every time, so
// installing it again replaces the accounts rather than adding two more.
func (h *handler) profile(w http.ResponseWriter, r *http.Request, user string) {
	o := originOf(r)
	p := profileData{
		Name: "Stratus (" + o.Host + ")", Host: o.Host, Port: o.Port, TLS: o.TLS, User: user,
		ID:   "es.stratus." + o.Host + "." + user,
		UUID: stableUUID(o.Host, user, "profile"),
	}
	if h.calendars != nil {
		p.Accounts = append(p.Accounts, p.account("caldav", "CalDAV", calendarsPrefix))
	}
	if h.contacts != nil {
		p.Accounts = append(p.Accounts, p.account("carddav", "CardDAV", contactsPrefix))
	}

	var buf bytes.Buffer
	if err := profileTemplate.Execute(&buf, p); err != nil {
		slog.Error("rendering a profile", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-apple-aspen-config")
	w.Header().Set("Content-Disposition", `attachment; filename="stratus.mobileconfig"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

type profileData struct {
	Name, Host, User, ID, UUID string
	Port                       int
	TLS                        bool
	Accounts                   []profileAccount
}

// profileAccount is one payload. Key is Apple's prefix for its fields, which
// differ between the two only by it.
type profileAccount struct {
	Type, Key, ID, UUID, Principal string
}

func (p profileData) account(kind, key, principal string) profileAccount {
	return profileAccount{
		Type: "com.apple." + kind + ".account", Key: key,
		ID:        p.ID + "." + kind,
		UUID:      stableUUID(p.Host, p.User, kind),
		Principal: principal,
	}
}

// stableUUID is a UUID that is the same for the same parts, shaped as a
// version 5 one, which is all a profile asks of it.
func stableUUID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	sum[6] = sum[6]&0x0f | 0x50
	sum[8] = sum[8]&0x3f | 0x80
	return fmt.Sprintf("%X-%X-%X-%X-%X", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

var profileTemplate = texttemplate.Must(texttemplate.New("profile").Funcs(texttemplate.FuncMap{
	"x": xmlText,
}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PayloadType</key><string>Configuration</string>
	<key>PayloadVersion</key><integer>1</integer>
	<key>PayloadIdentifier</key><string>{{x .ID}}</string>
	<key>PayloadUUID</key><string>{{.UUID}}</string>
	<key>PayloadDisplayName</key><string>{{x .Name}}</string>
	<key>PayloadDescription</key><string>Calendars and contacts on {{x .Host}}</string>
	<key>PayloadContent</key>
	<array>{{range .Accounts}}
		<dict>
			<key>PayloadType</key><string>{{.Type}}</string>
			<key>PayloadVersion</key><integer>1</integer>
			<key>PayloadIdentifier</key><string>{{x .ID}}</string>
			<key>PayloadUUID</key><string>{{.UUID}}</string>
			<key>PayloadDisplayName</key><string>{{.Key}}</string>
			<key>{{.Key}}AccountDescription</key><string>{{x $.Name}}</string>
			<key>{{.Key}}HostName</key><string>{{x $.Host}}</string>
			<key>{{.Key}}Port</key><integer>{{$.Port}}</integer>
			<key>{{.Key}}UseSSL</key>{{if $.TLS}}<true/>{{else}}<false/>{{end}}
			<key>{{.Key}}PrincipalURL</key><string>{{.Principal}}</string>
			<key>{{.Key}}Username</key><string>{{x $.User}}</string>
		</dict>{{end}}
	</array>
</dict>
</plist>
`))

func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
