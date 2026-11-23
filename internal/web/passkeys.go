package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/stratus-private-cloud/stratus-backend/internal/auth"
	"github.com/stratus-private-cloud/stratus-backend/internal/db"
)

// Passkeys sign in to the web UI as the password does (#532), and nowhere
// else: WebDAV, CalDAV and OpenSubsonic clients keep Basic, because none of
// them can hold one.
//
// **What the browser hands back travels as a form field**, the standard JSON
// of a PublicKeyCredential posted in a plain POST, and the page that started
// it was rendered with the options in an attribute. Nothing here answers in
// JSON, which is principle 2's line; the script only fills a field and submits.
//
// **A passkey is bound to the host it was made on.** The relying party is
// whatever Host the browser asked for, kept with each passkey, so one made at
// cloud.example does nothing at the machine's other name -- which is what
// WebAuthn would do anyway, and saves a variable saying which name is the
// real one. The origin is https, or http on localhost, which are the only
// places a browser offers WebAuthn at all.
const (
	passkeysPath     = "/settings/passkeys"
	passkeyLoginPath = "/login/passkey"
)

// passkeyNameMax is how long a passkey's name may be, in characters.
const passkeyNameMax = 64

// The ceremonies in flight: a challenge handed to a page, waiting for the
// browser to sign it. Kept here rather than in a cookie because one instance
// is a decision (principle 6) and memory makes each one single-use for free;
// a restart costs whoever was halfway through a retry.
const (
	ceremonyTTL = 5 * time.Minute
	// ceremoniesMax bounds what an unauthenticated page can make the server
	// hold: every view of the sign-in form starts one.
	ceremoniesMax = 256
)

type ceremony struct {
	data    webauthn.SessionData
	rpID    string
	expires time.Time
}

type ceremonies struct {
	mu sync.Mutex
	m  map[string]ceremony
}

func (c *ceremonies) start(rpID string, data *webauthn.SessionData, now time.Time) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]ceremony{}
	}
	var oldest string
	for token, held := range c.m {
		if now.After(held.expires) {
			delete(c.m, token)
			continue
		}
		if oldest == "" || held.expires.Before(c.m[oldest].expires) {
			oldest = token
		}
	}
	if len(c.m) >= ceremoniesMax {
		delete(c.m, oldest)
	}
	token := rand.Text()
	c.m[token] = ceremony{data: *data, rpID: rpID, expires: now.Add(ceremonyTTL)}
	return token
}

// take hands a ceremony over once and forgets it, so a signed answer cannot
// be posted twice.
func (c *ceremonies) take(token, rpID string, now time.Time) (webauthn.SessionData, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	held, ok := c.m[token]
	delete(c.m, token)
	if !ok || held.rpID != rpID || now.After(held.expires) {
		return webauthn.SessionData{}, false
	}
	return held.data, true
}

// errPasskey is every way a passkey fails to sign somebody in, said once:
// which check refused it is the log's business, not the page's.
var errPasskey = errors.New("the passkey was not accepted")

// relyingParty is WebAuthn as this request's host sees it.
func relyingParty(r *http.Request) (*webauthn.WebAuthn, string, error) {
	host := r.Host
	rpID := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		rpID = h
	}
	rpID = strings.ToLower(rpID)
	origins := []string{"https://" + host}
	if rpID == "localhost" {
		origins = append(origins, "http://"+host)
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID: rpID, RPDisplayName: "Stratus", RPOrigins: origins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationPreferred,
		},
	})
	return wa, rpID, err
}

// passkeyHandle is the user handle every passkey here is made with. One user
// for good (principle 4), and a constant rather than the username so that
// renaming the user does not orphan every passkey.
var passkeyHandle = []byte("stratus")

// passkeyUser is the one user, holding the passkeys made for one host.
type passkeyUser struct {
	name  string
	creds []webauthn.Credential
}

func (u passkeyUser) WebAuthnID() []byte                         { return passkeyHandle }
func (u passkeyUser) WebAuthnName() string                       { return u.name }
func (u passkeyUser) WebAuthnDisplayName() string                { return u.name }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// passkeysFor is the stored passkeys made for rpID, and the user holding them.
func (h *handler) passkeysFor(ctx context.Context, rpID string) ([]db.Passkey, passkeyUser, error) {
	all, err := h.passkeys.Passkeys(ctx)
	if err != nil {
		return nil, passkeyUser{}, err
	}
	mine, user := h.holding(ctx, all, rpID)
	return mine, user, nil
}

// holding is those of all made for rpID, and the user holding them. One that
// does not decode is left out rather than failing the page: it can still be
// removed.
func (h *handler) holding(ctx context.Context, all []db.Passkey, rpID string) ([]db.Passkey, passkeyUser) {
	user := passkeyUser{name: h.owner}
	var mine []db.Passkey
	for _, p := range all {
		if p.RPID != rpID {
			continue
		}
		var cred webauthn.Credential
		if err := json.Unmarshal(p.Credential, &cred); err != nil {
			slog.WarnContext(ctx, "a stored passkey does not decode", "name", p.Name, "err", err)
			continue
		}
		mine = append(mine, p)
		user.creds = append(user.creds, cred)
	}
	return mine, user
}

// begin keeps a ceremony the library started and answers what the page
// carries of it: the options as JSON for an attribute, which the template
// escapes, and the token the form posts back.
func (h *handler) begin(rpID string, options any, data *webauthn.SessionData, err error) (attr, token string, _ error) {
	if err != nil {
		return "", "", err
	}
	b, err := json.Marshal(options)
	if err != nil {
		return "", "", err
	}
	return string(b), h.ceremonies.start(rpID, data, time.Now()), nil
}

// withPasskey adds a passkey sign-in to the login page, when there is a
// passkey for this host to sign in with. Anything that goes wrong leaves the
// page with the password alone, which is the page it was before.
func (h *handler) withPasskey(r *http.Request, v view) view {
	if h.passkeys == nil {
		return v
	}
	wa, rpID, err := relyingParty(r)
	if err != nil {
		return v
	}
	mine, _, err := h.passkeysFor(r.Context(), rpID)
	if err != nil || len(mine) == 0 {
		return v
	}
	options, data, err := wa.BeginDiscoverableLogin()
	attr, token, err := h.begin(rpID, options, data, err)
	if err != nil {
		slog.WarnContext(r.Context(), "starting a passkey sign-in", "err", err)
		return v
	}
	v.PasskeyOptions, v.Ceremony = attr, token
	return v
}

// passkeyLogin is the form the passkey button posts.
func (h *handler) passkeyLogin(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.PostFormValue("next"))
	user, err := h.checkPasskey(r)
	switch {
	case errors.Is(err, auth.ErrTooManyAttempts):
		w.Header().Set("Retry-After", "2")
		h.render(w, http.StatusTooManyRequests, pageLogin, h.withPasskey(r, view{
			Title: "Sign in", Next: next, Error: "Too many attempts. Try again in a moment.",
		}))
		return
	case errors.Is(err, errPasskey):
		h.render(w, http.StatusUnauthorized, pageLogin, h.withPasskey(r, view{
			Title: "Sign in", Next: next, Error: "That passkey did not sign you in.",
		}))
		return
	case err != nil:
		h.fail(w, r, "", err)
		return
	}
	value, expires := h.sessions.Issue(user, time.Now())
	setSession(w, r, value, expires)
	redirectLocal(w, r, next)
}

// checkPasskey verifies a signed challenge and answers who it signs in. A
// refusal is errPasskey, held by the same throttle a wrong password is, so a
// passkey is not a second budget of guesses; a database that cannot answer is
// not a refusal and is not counted.
func (h *handler) checkPasskey(r *http.Request) (string, error) {
	ctx := r.Context()
	wa, rpID, err := relyingParty(r)
	if err != nil {
		return "", h.refused(ctx, err.Error())
	}
	data, ok := h.ceremonies.take(r.PostFormValue("ceremony"), rpID, time.Now())
	if !ok {
		return "", h.refused(ctx, "no such ceremony")
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes([]byte(r.PostFormValue("credential")))
	if err != nil {
		return "", h.refused(ctx, err.Error())
	}
	mine, user, err := h.passkeysFor(ctx, rpID)
	if err != nil {
		return "", err
	}
	_, cred, err := wa.ValidatePasskeyLogin(func(_, _ []byte) (webauthn.User, error) { return user, nil }, data, parsed)
	if err != nil {
		return "", h.refused(ctx, err.Error())
	}

	// The counter and the flags a sign-in moved, kept for the next one. A
	// passkey forgotten in between is not resurrected.
	for _, p := range mine {
		if !bytes.Equal(p.ID, cred.ID) {
			continue
		}
		encoded, err := json.Marshal(cred)
		if err != nil {
			return "", err
		}
		p.Credential, p.LastUsed = encoded, time.Now().UTC()
		if err := h.passkeys.PutPasskey(ctx, p); err != nil {
			return "", err
		}
	}
	return h.owner, nil
}

// refused counts a failed passkey against the shared throttle and answers
// errPasskey, or ErrTooManyAttempts when the throttle says so.
func (h *handler) refused(ctx context.Context, why string) error {
	slog.InfoContext(ctx, "a passkey sign-in was refused", "why", why)
	if t, ok := h.verifier.(interface {
		Fail(ctx context.Context, err error) error
	}); ok {
		if err := t.Fail(ctx, errPasskey); errors.Is(err, auth.ErrTooManyAttempts) {
			return err
		}
	}
	return errPasskey
}

// passkeyRow is one passkey on the settings page.
type passkeyRow struct {
	ID, Name, Host string
	Created        time.Time
	LastUsed       time.Time
}

// passkeysView is the passkeys page: what there is, and the ceremony that
// adds one for this host.
type passkeysView struct {
	Rows     []passkeyRow
	Host     string
	Ceremony string
	Options  string
	Name     string
}

func (h *handler) passkeysPage(w http.ResponseWriter, r *http.Request, user string) {
	h.renderPasskeys(w, r, user, http.StatusOK, "", "")
}

func (h *handler) renderPasskeys(w http.ResponseWriter, r *http.Request, user string, status int, problem, notice string) {
	wa, rpID, err := relyingParty(r)
	if err != nil {
		h.fail(w, r, user, err)
		return
	}
	all, err := h.passkeys.Passkeys(r.Context())
	if err != nil {
		h.fail(w, r, user, err)
		return
	}
	pv := &passkeysView{Host: rpID}
	if problem != "" {
		pv.Name = r.PostFormValue("name")
	}
	for _, p := range all {
		pv.Rows = append(pv.Rows, passkeyRow{
			ID: base64.RawURLEncoding.EncodeToString(p.ID), Name: p.Name, Host: p.RPID, Created: p.Created, LastUsed: p.LastUsed,
		})
	}
	_, owner := h.holding(r.Context(), all, rpID)
	options, data, err := wa.BeginRegistration(owner,
		webauthn.WithExclusions(webauthn.Credentials(owner.creds).CredentialDescriptors()),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired))
	if pv.Options, pv.Ceremony, err = h.begin(rpID, options, data, err); err != nil {
		h.fail(w, r, user, err)
		return
	}
	h.render(w, status, pagePasskeys, view{
		Title: "Passkeys", User: user, Passkeys: pv, Error: problem, Notice: notice,
	})
}

// addPasskey stores the passkey a browser just made. The password is asked
// for again: a session is a cookie somebody may have walked off with, and a
// passkey added with it would outlive the password change that revokes it.
func (h *handler) addPasskey(w http.ResponseWriter, r *http.Request, user string) {
	ctx := r.Context()
	if err := h.verifier.Verify(ctx, user, r.PostFormValue("password")); err != nil {
		problem := "That is not the password."
		if errors.Is(err, auth.ErrTooManyAttempts) {
			problem = "Too many attempts. Try again in a moment."
		}
		h.renderPasskeys(w, r, user, http.StatusUnauthorized, problem, "")
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" || utf8.RuneCountInString(name) > passkeyNameMax {
		h.renderPasskeys(w, r, user, http.StatusBadRequest, "A passkey needs a name, of 64 characters at most.", "")
		return
	}
	wa, rpID, err := relyingParty(r)
	if err != nil {
		h.fail(w, r, user, err)
		return
	}
	data, ok := h.ceremonies.take(r.PostFormValue("ceremony"), rpID, time.Now())
	if !ok {
		h.renderPasskeys(w, r, user, http.StatusBadRequest, "That took too long. Try again.", "")
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes([]byte(r.PostFormValue("credential")))
	if err != nil {
		h.renderPasskeys(w, r, user, http.StatusBadRequest, "The browser did not make a passkey.", "")
		return
	}
	_, owner, err := h.passkeysFor(ctx, rpID)
	if err != nil {
		h.fail(w, r, user, err)
		return
	}
	cred, err := wa.CreateCredential(owner, data, parsed)
	if err != nil {
		slog.InfoContext(ctx, "a new passkey was refused", "err", err)
		h.renderPasskeys(w, r, user, http.StatusBadRequest, "That passkey could not be checked.", "")
		return
	}
	encoded, err := json.Marshal(cred)
	if err != nil {
		h.fail(w, r, user, err)
		return
	}
	if err := h.passkeys.PutPasskey(ctx, db.Passkey{
		ID: cred.ID, RPID: rpID, Name: name, Credential: encoded, Created: time.Now().UTC(),
	}); err != nil {
		h.fail(w, r, user, err)
		return
	}
	h.renderPasskeys(w, r, user, http.StatusOK, "", "The passkey was added.")
}

// deletePasskey forgets one. Sessions it opened stay open: a session is
// signed, not stored, and only a new password ends them.
func (h *handler) deletePasskey(w http.ResponseWriter, r *http.Request, user string) {
	id, err := base64.RawURLEncoding.DecodeString(r.PostFormValue("id"))
	if err != nil {
		h.badRequest(w, user, "That is not a passkey.")
		return
	}
	switch err := h.passkeys.DeletePasskey(r.Context(), id); {
	case errors.Is(err, db.ErrNotFound):
		h.notFound(w, r)
		return
	case err != nil:
		h.fail(w, r, user, err)
		return
	}
	h.renderPasskeys(w, r, user, http.StatusOK, "", "The passkey was removed.")
}
