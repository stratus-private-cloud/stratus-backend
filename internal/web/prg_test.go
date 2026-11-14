package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// sent is a form submitted by a browser, and the answer as it came: not
// followed, which is what these tests are about.
func sent(t *testing.T, h http.Handler, target string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestAFormThatDrawsAPageRedirectsToIt is #501: the link a share made is a
// page drawn from a POST, so a reload sent the form again and made another
// link. Now the POST redirects, and a reload of where it went shows the same
// link and makes nothing.
func TestAFormThatDrawsAPageRedirectsToIt(t *testing.T) {
	t.Parallel()
	h, s, _ := browserOver(t)
	write(t, s, "holiday.txt", "the whole film")
	cookie := signIn(t, h)

	rec := sent(t, h, "/share/holiday.txt", url.Values{"life": {"7d"}}, cookie)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/share/holiday.txt?flash=") {
		t.Fatalf("sharing = %d to %q, want a redirect to the page it drew", rec.Code, loc)
	}
	first := get(t, h, loc, cookie)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "holiday.txt") {
		t.Fatalf("the page it drew = %d: %s", first.Code, first.Body)
	}
	if again := get(t, h, loc, cookie); again.Body.String() != first.Body.String() {
		t.Error("a reload showed a different page from the one the form drew")
	}

	// It is that session's: anybody else gets the address as it is without
	// the parameter, which is the login form.
	if other := get(t, h, loc); other.Code != http.StatusSeeOther ||
		!strings.HasPrefix(other.Header().Get("Location"), "/login") {
		t.Errorf("the kept page with no session = %d to %q", other.Code, other.Header().Get("Location"))
	}
	// And a parameter it does not know is no parameter at all.
	if unknown := get(t, h, "/share/holiday.txt?flash=nothing", cookie); strings.Contains(unknown.Body.String(), "https://") {
		t.Error("an unknown flash showed a link")
	}
}

// TestARefusedFormRedirectsToo: a problem is a page like any other, so a
// form that could not be done is not sent again by a reload either.
func TestARefusedFormRedirectsToo(t *testing.T) {
	t.Parallel()
	h, _, _ := browserOver(t)
	cookie := signIn(t, h)

	rec := sent(t, h, "/folders/", url.Values{"name": {".."}}, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("a refused folder = %d, want a redirect", rec.Code)
	}
	if shown := get(t, h, rec.Header().Get("Location"), cookie); shown.Code != http.StatusBadRequest {
		t.Errorf("what it redirected to = %d, want the refusal it drew", shown.Code)
	}
}

// TestALoginThatFailsRedirects: the one form with no session to keep a page
// for, and the one whose page holds nothing but the form.
func TestALoginThatFailsRedirects(t *testing.T) {
	t.Parallel()
	h, _, _ := browserOver(t)

	rec := sent(t, h, "/login", url.Values{"username": {username}, "password": {"wrong"}}, nil)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login?flash=") {
		t.Fatalf("a wrong password = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	shown := get(t, h, rec.Header().Get("Location"))
	if shown.Code != http.StatusUnauthorized || !strings.Contains(shown.Body.String(), "Wrong username or password.") {
		t.Errorf("the login page after a wrong password = %d: %s", shown.Code, shown.Body)
	}
}

// TestWhatIsNotANavigationIsAnsweredAsItWas: htmx and a client with Basic
// get the page itself, since nothing of theirs goes into a history.
func TestWhatIsNotANavigationIsAnsweredAsItWas(t *testing.T) {
	t.Parallel()
	h, _, _ := browserOver(t)
	cookie := signIn(t, h)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/folders/",
		strings.NewReader(url.Values{"name": {".."}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("an htmx POST = %d, want the page it drew", rec.Code)
	}
}
