package web_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	htmlstd "html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/stratus-private-cloud/stratus-backend/internal/auth"
	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/db/dbtest"
	"github.com/stratus-private-cloud/stratus-backend/internal/web"
)

// The passkey tests drive the pages with an authenticator in software: a P-256
// key that answers the options a page rendered the way a browser's would, so
// what is verified is the library's real check and not a stub of it. The host
// is httptest's, example.com, so the origin is https://example.com.
const passkeyOrigin = "https://example.com"

var b64 = base64.RawURLEncoding

type authenticator struct {
	key     *ecdsa.PrivateKey
	id      []byte
	counter uint32
	// handle is the user handle the authenticator was given at creation.
	handle []byte
}

func newAuthenticator(t *testing.T) *authenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &authenticator{key: key, id: id}
}

// optionsOf reads the options and the ceremony out of the form a page drew
// with data-passkey set to kind.
func optionsOf(t *testing.T, body, kind string) (map[string]any, string) {
	t.Helper()
	form := regexp.MustCompile(`(?s)<form[^>]*data-passkey="` + kind + `"[^>]*data-options="([^"]*)".*?name="ceremony" value="([^"]*)"`).FindStringSubmatch(body)
	if form == nil {
		t.Fatalf("no %s passkey form on the page:\n%s", kind, body)
	}
	var options struct {
		PublicKey map[string]any `json:"publicKey"`
	}
	if err := json.Unmarshal([]byte(htmlstd.UnescapeString(form[1])), &options); err != nil {
		t.Fatal(err)
	}
	return options.PublicKey, form[2]
}

func clientData(kind string, options map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"type": kind, "challenge": options["challenge"], "origin": passkeyOrigin})
	return b
}

func (a *authenticator) authData(rpID string, flags byte, attested []byte) []byte {
	hash := sha256.Sum256([]byte(rpID))
	out := append(hash[:], flags)
	a.counter++
	out = binary.BigEndian.AppendUint32(out, a.counter)
	return append(out, attested...)
}

// create answers a page's creation options with a new credential.
func (a *authenticator) create(t *testing.T, options map[string]any) string {
	t.Helper()
	rp := options["rp"].(map[string]any)
	handle, err := b64.DecodeString(options["user"].(map[string]any)["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	a.handle = handle
	cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: a.key.X.FillBytes(make([]byte, 32)), -3: a.key.Y.FillBytes(make([]byte, 32))})
	if err != nil {
		t.Fatal(err)
	}
	// No AAGUID, then the ID's length, the ID and the key.
	attested := slices.Concat(make([]byte, 16), binary.BigEndian.AppendUint16(nil, uint16(len(a.id))), a.id, cose)
	// UP, UV and AT: present, verified, and a credential follows.
	auth := a.authData(rp["id"].(string), 0x01|0x04|0x40, attested)
	object, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": auth})
	if err != nil {
		t.Fatal(err)
	}
	cred, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.id), "rawId": b64.EncodeToString(a.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(clientData("webauthn.create", options)),
			"attestationObject": b64.EncodeToString(object),
		},
	})
	return string(cred)
}

// get answers a page's request options by signing its challenge.
func (a *authenticator) get(t *testing.T, options map[string]any) string {
	t.Helper()
	auth := a.authData(options["rpId"].(string), 0x01|0x04, nil)
	data := clientData("webauthn.get", options)
	hash := sha256.Sum256(data)
	digest := sha256.Sum256(append(append([]byte{}, auth...), hash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	cred, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.id), "rawId": b64.EncodeToString(a.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON": b64.EncodeToString(data), "authenticatorData": b64.EncodeToString(auth),
			"signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString(a.handle),
		},
	})
	return string(cred)
}

// browserWithPasskeys is the UI over a database that holds passkeys, and the
// throttle the server puts in front of the password.
func browserWithPasskeys(t *testing.T) (http.Handler, db.Store) {
	t.Helper()
	h, meta, _ := passkeysOver(t, auth.ThrottleConfig{Every: 0, Burst: 1 << 20, MaxWait: 0})
	return h, meta
}

// passkeysOver is the same over a throttle of the test's shape, and a switch
// that breaks one call of the passkeys' store by name from then on.
func passkeysOver(t *testing.T, limit auth.ThrottleConfig) (http.Handler, db.Store, func(method string)) {
	t.Helper()
	s, thumbs, meta := pieces(t)
	creds := credentials()
	store := &switchable{to: meta}
	return web.Handler(web.Deps{
		Version: version, BuildDate: buildDate, Verifier: auth.NewThrottle(creds, limit),
		Sessions: auth.NewSessions(creds, auth.DefaultSessionTTL), Shares: auth.NewShares(creds),
		Files: s, Thumbs: thumbs, Index: meta, Playlists: playlistsOf(meta), Indexing: indexing(meta),
		Passkeys: store, Owner: username, TrashRetention: aMonth,
	}), meta, func(method string) { store.to = dbtest.FailOn(t, meta, method) }
}

// switchable is a passkeys store a test can swap for a failing one halfway.
type switchable struct{ to db.Passkeys }

func (s *switchable) Passkeys(ctx context.Context) ([]db.Passkey, error) { return s.to.Passkeys(ctx) }
func (s *switchable) PutPasskey(ctx context.Context, p db.Passkey) error {
	return s.to.PutPasskey(ctx, p)
}
func (s *switchable) DeletePasskey(ctx context.Context, id []byte) error {
	return s.to.DeletePasskey(ctx, id)
}

// addPasskey makes a passkey through the settings page, as a person does.
func addPasskey(t *testing.T, h http.Handler, cookie *http.Cookie, a *authenticator, name string) int {
	t.Helper()
	options, ceremony := optionsOf(t, get(t, h, "/settings/passkeys", cookie).Body.String(), "create")
	rec := post(t, h, "/settings/passkeys", url.Values{
		"ceremony": {ceremony}, "credential": {a.create(t, options)}, "name": {name}, "password": {examplePassword},
	}, cookie)
	return rec.Code
}

func TestAPasskeySignsIn(t *testing.T) {
	t.Parallel()
	h, meta := browserWithPasskeys(t)
	cookie := signIn(t, h)

	if strings.Contains(get(t, h, "/login").Body.String(), `data-passkey="get"`) {
		t.Error("the sign-in page offers a passkey before there is one")
	}

	a := newAuthenticator(t)
	if code := addPasskey(t, h, cookie, a, "Phone"); code != http.StatusOK {
		t.Fatalf("adding a passkey = %d", code)
	}
	page := get(t, h, "/settings/passkeys", cookie).Body.String()
	has(t, page, "Phone", "example.com", "never used")
	stored, err := meta.Passkeys(t.Context())
	if err != nil || len(stored) != 1 || stored[0].RPID != "example.com" {
		t.Fatalf("Passkeys = %+v, %v", stored, err)
	}

	options, ceremony := optionsOf(t, get(t, h, "/login").Body.String(), "get")
	signed := a.get(t, options)
	rec := post(t, h, "/login/passkey", url.Values{"ceremony": {ceremony}, "credential": {signed}, "next": {"/status"}})
	session := sessionCookie(rec)
	if session == nil {
		t.Fatalf("a passkey set no cookie: %d %s", rec.Code, rec.Body)
	}
	if got := get(t, h, "/status", session); got.Code != http.StatusOK {
		t.Errorf("the session a passkey made = %d", got.Code)
	}
	if stored, _ = meta.Passkeys(t.Context()); stored[0].LastUsed.IsZero() {
		t.Error("signing in did not say the passkey was used")
	}

	// The same answer twice is a replay, and the ceremony it answered is gone.
	again := post(t, h, "/login/passkey", url.Values{"ceremony": {ceremony}, "credential": {signed}})
	if again.Code != http.StatusUnauthorized || sessionCookie(again) != nil {
		t.Errorf("a replayed passkey = %d", again.Code)
	}
}

func TestAPasskeyThatIsNotOneDoesNotSignIn(t *testing.T) {
	t.Parallel()
	h, _ := browserWithPasskeys(t)
	cookie := signIn(t, h)
	a := newAuthenticator(t)
	addPasskey(t, h, cookie, a, "Phone")

	options, ceremony := optionsOf(t, get(t, h, "/login").Body.String(), "get")
	// Another key claiming the same credential: the signature does not verify.
	impostor := newAuthenticator(t)
	impostor.id, impostor.handle = a.id, a.handle
	rec := post(t, h, "/login/passkey", url.Values{"ceremony": {ceremony}, "credential": {impostor.get(t, options)}})
	if rec.Code != http.StatusUnauthorized || sessionCookie(rec) != nil {
		t.Errorf("a passkey signed by the wrong key = %d", rec.Code)
	}
	has(t, rec.Body.String(), "That passkey did not sign you in.")

	for name, form := range map[string]url.Values{
		"no ceremony":   {"credential": {a.get(t, options)}},
		"no credential": {"ceremony": {ceremony}},
		"garbage":       {"ceremony": {ceremony}, "credential": {"{"}},
	} {
		if rec := post(t, h, "/login/passkey", form); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401", name, rec.Code)
		}
	}
}

// TestAFailedPasskeyCountsAgainstTheThrottle: a passkey is not a second
// budget of guesses beside the password.
func TestAFailedPasskeyCountsAgainstTheThrottle(t *testing.T) {
	t.Parallel()
	h, _, _ := passkeysOver(t, auth.ThrottleConfig{Every: time.Hour, Burst: 1, MaxWait: 0})
	if rec := post(t, h, "/login/passkey", url.Values{"ceremony": {"x"}, "credential": {"{}"}}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the first failed passkey = %d, want 401", rec.Code)
	}
	if rec := post(t, h, "/login", url.Values{"username": {username}, "password": {"wrong"}}); rec.Code != http.StatusTooManyRequests {
		t.Errorf("a wrong password after a failed passkey = %d, want 429", rec.Code)
	}
	if rec := post(t, h, "/login/passkey", url.Values{"ceremony": {"x"}}); rec.Code != http.StatusTooManyRequests ||
		!strings.Contains(rec.Body.String(), "Too many attempts") {
		t.Errorf("a second failed passkey = %d, want 429", rec.Code)
	}
}

// TestAPasskeyThatCannotBeMadeIsNotAdded: each way the browser's answer can
// be wrong says so on the page and stores nothing.
func TestAPasskeyThatCannotBeMadeIsNotAdded(t *testing.T) {
	t.Parallel()
	h, meta := browserWithPasskeys(t)
	cookie := signIn(t, h)
	a := newAuthenticator(t)
	options, ceremony := optionsOf(t, get(t, h, "/settings/passkeys", cookie).Body.String(), "create")
	_, other := optionsOf(t, get(t, h, "/settings/passkeys", cookie).Body.String(), "create")
	made := a.create(t, options)

	for name, tc := range map[string]struct {
		form url.Values
		says string
	}{
		"a ceremony that is not there":   {url.Values{"ceremony": {"x"}, "credential": {made}}, "That took too long."},
		"no credential":                  {url.Values{"ceremony": {ceremony}, "credential": {"{"}}, "The browser did not make a passkey."},
		"an answer to another challenge": {url.Values{"ceremony": {other}, "credential": {made}}, "That passkey could not be checked."},
		"a name too long":                {url.Values{"ceremony": {ceremony}, "credential": {made}, "name": {strings.Repeat("x", 65)}}, "64 characters"},
	} {
		if !tc.form.Has("name") {
			tc.form.Set("name", "Phone")
		}
		tc.form.Set("password", examplePassword)
		rec := post(t, h, "/settings/passkeys", tc.form, cookie)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.says) {
			t.Errorf("%s = %d, want 400 saying %q", name, rec.Code, tc.says)
		}
	}
	if stored, _ := meta.Passkeys(t.Context()); len(stored) != 0 {
		t.Errorf("a passkey was stored: %+v", stored)
	}
}

// TestAStoreThatCannotAnswerIsNotARefusal: the pages fail as pages do, the
// sign-in form keeps the password, and nothing counts against the throttle.
func TestAStoreThatCannotAnswerIsNotARefusal(t *testing.T) {
	t.Parallel()
	h, _, breakStore := passkeysOver(t, auth.ThrottleConfig{Every: time.Hour, Burst: 1, MaxWait: 0})
	cookie := signIn(t, h)
	a := newAuthenticator(t)
	if code := addPasskey(t, h, cookie, a, "Phone"); code != http.StatusOK {
		t.Fatalf("adding a passkey = %d", code)
	}
	options, ceremony := optionsOf(t, get(t, h, "/login").Body.String(), "get")

	breakStore("PutPasskey")
	rec := post(t, h, "/login/passkey", url.Values{"ceremony": {ceremony}, "credential": {a.get(t, options)}})
	if rec.Code != http.StatusInternalServerError || sessionCookie(rec) != nil {
		t.Errorf("signing in when the passkey cannot be kept = %d", rec.Code)
	}
	options, ceremony = optionsOf(t, get(t, h, "/login").Body.String(), "get")
	creation, made := optionsOf(t, get(t, h, "/settings/passkeys", cookie).Body.String(), "create")
	if rec := post(t, h, "/settings/passkeys", url.Values{
		"ceremony": {made}, "credential": {newAuthenticator(t).create(t, creation)}, "name": {"Laptop"}, "password": {examplePassword},
	}, cookie); rec.Code == http.StatusOK {
		t.Error("a passkey that could not be stored was added")
	}

	breakStore("Passkeys")
	if body := get(t, h, "/login").Body.String(); strings.Contains(body, `data-passkey="get"`) || !strings.Contains(body, `name="password"`) {
		t.Error("the sign-in page without its passkeys is not the password form")
	}
	if rec := get(t, h, "/settings/passkeys", cookie); rec.Code != http.StatusInternalServerError {
		t.Errorf("the passkeys page without its store = %d, want 500", rec.Code)
	}
	if rec := post(t, h, "/login/passkey", url.Values{"ceremony": {ceremony}, "credential": {a.get(t, options)}}); rec.Code != http.StatusInternalServerError {
		t.Errorf("signing in without the store = %d, want 500", rec.Code)
	}

	breakStore("DeletePasskey")
	if rec := post(t, h, "/settings/passkeys/delete", url.Values{"id": {b64.EncodeToString(a.id)}}, cookie); rec.Code != http.StatusInternalServerError {
		t.Errorf("removing a passkey without the store = %d, want 500", rec.Code)
	}
}

func TestAddingAPasskeyAsksForThePassword(t *testing.T) {
	t.Parallel()
	h, meta := browserWithPasskeys(t)
	cookie := signIn(t, h)
	a := newAuthenticator(t)

	options, ceremony := optionsOf(t, get(t, h, "/settings/passkeys", cookie).Body.String(), "create")
	rec := post(t, h, "/settings/passkeys", url.Values{
		"ceremony": {ceremony}, "credential": {a.create(t, options)}, "name": {"Phone"}, "password": {"wrong"},
	}, cookie)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("adding a passkey with the wrong password = %d, want 401", rec.Code)
	}
	if stored, _ := meta.Passkeys(t.Context()); len(stored) != 0 {
		t.Errorf("a passkey was added without the password: %+v", stored)
	}
	if code := addPasskey(t, h, cookie, a, ""); code != http.StatusBadRequest {
		t.Errorf("a passkey with no name = %d, want 400", code)
	}
}

func TestRemovingAPasskey(t *testing.T) {
	t.Parallel()
	h, meta := browserWithPasskeys(t)
	cookie := signIn(t, h)
	a := newAuthenticator(t)
	addPasskey(t, h, cookie, a, "Phone")

	rec := post(t, h, "/settings/passkeys/delete", url.Values{"id": {b64.EncodeToString(a.id)}}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("removing a passkey = %d", rec.Code)
	}
	has(t, rec.Body.String(), "The passkey was removed.")
	if stored, _ := meta.Passkeys(t.Context()); len(stored) != 0 {
		t.Errorf("Passkeys after removing it = %+v", stored)
	}
	if strings.Contains(get(t, h, "/login").Body.String(), `data-passkey="get"`) {
		t.Error("the sign-in page offers a passkey that was removed")
	}
	if rec := post(t, h, "/settings/passkeys/delete", url.Values{"id": {b64.EncodeToString(a.id)}}, cookie); rec.Code != http.StatusNotFound {
		t.Errorf("removing it again = %d, want 404", rec.Code)
	}
	if rec := post(t, h, "/settings/passkeys/delete", url.Values{"id": {"%%"}}, cookie); rec.Code != http.StatusBadRequest {
		t.Errorf("removing what is not an id = %d, want 400", rec.Code)
	}
}

// TestAPasskeyBelongsToItsHost: one made at one name of the machine is not
// offered at another, which would not accept it anyway.
func TestAPasskeyBelongsToItsHost(t *testing.T) {
	t.Parallel()
	h, meta := browserWithPasskeys(t)
	for _, p := range []db.Passkey{
		{ID: []byte{1}, RPID: "elsewhere.example", Name: "Other", Credential: []byte("{}"), Created: time.Now()},
		// One for this host that no longer decodes is left out, not fatal.
		{ID: []byte{2}, RPID: "example.com", Name: "Broken", Credential: []byte("not json"), Created: time.Now()},
	} {
		if err := meta.PutPasskey(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(get(t, h, "/login").Body.String(), `data-passkey="get"`) {
		t.Error("the sign-in page offers a passkey made for another host")
	}
	cookie := signIn(t, h)
	has(t, get(t, h, "/settings/passkeys", cookie).Body.String(), "Other", "elsewhere.example", "Broken")
}

// TestAHostThatIsNotOneHasNoPasskeys: with no host there is no relying
// party, and the pages say so rather than offering one.
func TestAHostThatIsNotOneHasNoPasskeys(t *testing.T) {
	t.Parallel()
	h, _ := browserWithPasskeys(t)
	cookie := signIn(t, h)
	a := newAuthenticator(t)
	addPasskey(t, h, cookie, a, "Phone")

	nowhere := func(method, target string, form url.Values, cookies ...*http.Cookie) int {
		req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(form.Encode()))
		req.Host = ""
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if method == http.MethodGet && strings.Contains(rec.Body.String(), `data-passkey=`) {
			t.Errorf("%s %s with no host offers a passkey", method, target)
		}
		return rec.Code
	}
	nowhere(http.MethodGet, "/login", nil)

	// localhost is the one host a browser allows WebAuthn on over http.
	local := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/settings/passkeys", nil)
	local.Host = "localhost:8080"
	local.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, local)
	has(t, rec.Body.String(), "Add a passkey for localhost")
	if code := nowhere(http.MethodGet, "/settings/passkeys", nil, cookie); code != http.StatusInternalServerError {
		t.Errorf("the passkeys page with no host = %d, want 500", code)
	}
	if code := nowhere(http.MethodPost, "/login/passkey", url.Values{"ceremony": {"x"}}); code != http.StatusUnauthorized {
		t.Errorf("a passkey with no host = %d, want 401", code)
	}
	if code := nowhere(http.MethodPost, "/settings/passkeys", url.Values{"name": {"x"}, "password": {examplePassword}}, cookie); code != http.StatusInternalServerError {
		t.Errorf("adding a passkey with no host = %d, want 500", code)
	}
}

func TestWithoutPasskeysThereIsNoPage(t *testing.T) {
	t.Parallel()
	h, _ := browser(t)
	cookie := signIn(t, h)
	if rec := get(t, h, "/settings/passkeys", cookie); rec.Code != http.StatusNotFound {
		t.Errorf("the passkeys page with none to keep = %d, want 404", rec.Code)
	}
}
