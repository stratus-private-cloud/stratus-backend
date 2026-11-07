package web_test

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConnectingADeviceNeedsASession(t *testing.T) {
	t.Parallel()
	h := newHandler(t, nil)
	for _, target := range []string{"/connect", "/connect/stratus.mobileconfig"} {
		rec := get(t, h, target)
		if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
			t.Errorf("%s with no session = %d", target, rec.Code)
		}
	}
}

// TestTheConnectPageHandsThePhoneOver is #485: a profile for iOS, a link
// that opens DAVx5 with the server and the user in it, and no password.
func TestTheConnectPageHandsThePhoneOver(t *testing.T) {
	t.Parallel()
	h := newHandler(t, nil)
	cookie := signIn(t, h)

	body := getOver(t, h, "/connect", "https", "cloud.example:8443", cookie).Body.String()
	for _, want := range []string{
		`href="/connect/stratus.mobileconfig"`,
		`href="davx5://` + username + `@cloud.example:8443/"`,
		"https://cloud.example:8443/",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not carry %q", want)
		}
	}
	if strings.Contains(body, examplePassword) {
		t.Error("the page carries the password")
	}

	// Plain http is DAVx5's caldav://, which it reads as http.
	body = getOver(t, h, "/connect", "http", "cloud.example", cookie).Body.String()
	if !strings.Contains(body, `href="caldav://`+username+`@cloud.example/"`) {
		t.Error("a server over http is not offered to DAVx5 as one")
	}

	// And it is reachable from the menu, the calendar and the contacts.
	for _, from := range []string{"/files/", "/calendars/" + username + "/default/", "/contacts/" + username + "/contacts/"} {
		if !strings.Contains(get(t, h, from, cookie).Body.String(), `href="/connect"`) {
			t.Errorf("%s does not link to the page", from)
		}
	}
}

type plist struct {
	Dict plistDict `xml:"dict"`
}

// plistDict reads a dict's keys and the value after each as text, which is
// enough to ask a profile what it says.
type plistDict struct {
	Items []plistItem `xml:",any"`
}

type plistItem struct {
	XMLName xml.Name
	Text    string      `xml:",chardata"`
	Dicts   []plistDict `xml:"dict"`
}

func (d plistDict) get(key string) (plistItem, bool) {
	for i := 0; i+1 < len(d.Items); i++ {
		if d.Items[i].XMLName.Local == "key" && d.Items[i].Text == key {
			return d.Items[i+1], true
		}
	}
	return plistItem{}, false
}

func profileOver(t *testing.T, h http.Handler, scheme, host string) (plistDict, string) {
	t.Helper()
	rec := getOver(t, h, "/connect/stratus.mobileconfig", scheme, host, signIn(t, h))
	if rec.Code != http.StatusOK {
		t.Fatalf("the profile = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/x-apple-aspen-config" {
		t.Errorf("the profile is sent as %q", ct)
	}
	raw := rec.Body.String()
	var p plist
	if err := xml.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("the profile is not XML: %v\n%s", err, raw)
	}
	return p.Dict, raw
}

func TestTheProfileHoldsBothAccounts(t *testing.T) {
	t.Parallel()
	h := newHandler(t, nil)
	top, raw := profileOver(t, h, "https", "cloud.example")

	if strings.Contains(raw, examplePassword) {
		t.Error("the profile carries the password")
	}
	content, ok := top.get("PayloadContent")
	if !ok || len(content.Dicts) != 2 {
		t.Fatalf("the profile holds %d accounts, want 2", len(content.Dicts))
	}
	for i, prefix := range []string{"CalDAV", "CardDAV"} {
		account := content.Dicts[i]
		for key, want := range map[string]string{
			prefix + "HostName":     "cloud.example",
			prefix + "Port":         "443",
			prefix + "Username":     username,
			prefix + "PrincipalURL": map[string]string{"CalDAV": "/calendars/", "CardDAV": "/contacts/"}[prefix],
		} {
			if got, _ := account.get(key); got.Text != want {
				t.Errorf("%s = %q, want %q", key, got.Text, want)
			}
		}
		if got, _ := account.get(prefix + "UseSSL"); got.XMLName.Local != "true" {
			t.Errorf("%sUseSSL = <%s/> over https", prefix, got.XMLName.Local)
		}
	}

	// Installed twice it replaces itself, which is the identifiers holding.
	again, _ := profileOver(t, h, "https", "cloud.example")
	first, _ := top.get("PayloadUUID")
	second, _ := again.get("PayloadUUID")
	if first.Text == "" || first.Text != second.Text {
		t.Errorf("the profile's UUID moved: %q then %q", first.Text, second.Text)
	}
}

func TestTheProfileOverPlainHTTP(t *testing.T) {
	t.Parallel()
	h := newHandler(t, nil)
	top, _ := profileOver(t, h, "http", "cloud.example:8080")
	content, _ := top.get("PayloadContent")
	account := content.Dicts[0]
	if got, _ := account.get("CalDAVPort"); got.Text != "8080" {
		t.Errorf("CalDAVPort = %q", got.Text)
	}
	if got, _ := account.get("CalDAVUseSSL"); got.XMLName.Local != "false" {
		t.Errorf("CalDAVUseSSL = <%s/> over http", got.XMLName.Local)
	}
}

// getOver is get as the request arrives through a proxy: the host it was sent
// to and whether it was TLS, both of which the page reads.
func getOver(t *testing.T, h http.Handler, target, scheme, host string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	req.Host = host
	req.Header.Set("X-Forwarded-Proto", scheme)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
