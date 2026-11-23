package dbtest

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
)

// RunPasskeys executes the passkey cases against the repository built by
// newRepo.
func RunPasskeys(t *testing.T, newRepo func(t *testing.T) db.Repo) {
	t.Helper()

	cases := []struct {
		name string
		fn   func(t *testing.T, s db.Repo)
	}{
		{"a passkey comes back as it was stored, and a second write of its ID replaces it", passkeysRoundTrip},
		{"forgetting a passkey forgets that one, and one never stored is not found", passkeysDelete},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.fn(t, newRepo(t))
		})
	}
}

var passkeyAt = time.Date(2024, 9, 1, 20, 0, 0, 0, time.UTC)

func passkeysOf(t *testing.T, s db.Repo) []db.Passkey {
	t.Helper()
	got, err := s.Passkeys(t.Context())
	if err != nil {
		t.Fatalf("Passkeys: %v", err)
	}
	return got
}

func samePasskey(a, b db.Passkey) bool {
	return bytes.Equal(a.ID, b.ID) && a.RPID == b.RPID && a.Name == b.Name &&
		bytes.Equal(a.Credential, b.Credential) && a.Created.Equal(b.Created) && a.LastUsed.Equal(b.LastUsed)
}

func passkeysRoundTrip(t *testing.T, s db.Repo) {
	if got := passkeysOf(t, s); len(got) != 0 {
		t.Fatalf("Passkeys of nothing = %v", got)
	}
	// Bytes no text column would keep: a zero, and what is not UTF-8.
	phone := db.Passkey{ID: []byte{0, 0xff, 1}, RPID: "cloud.example", Name: "Phone", Credential: []byte{0, 0xfe}, Created: passkeyAt}
	laptop := db.Passkey{ID: []byte{2}, RPID: "cloud.example", Name: "Laptöp", Credential: []byte("{}"), Created: passkeyAt.Add(time.Hour)}
	for _, p := range []db.Passkey{laptop, phone} {
		if err := s.PutPasskey(t.Context(), p); err != nil {
			t.Fatalf("PutPasskey(%s): %v", p.Name, err)
		}
	}
	got := passkeysOf(t, s)
	if len(got) != 2 || !samePasskey(got[0], phone) || !samePasskey(got[1], laptop) {
		t.Fatalf("Passkeys = %+v, want the phone then the laptop", got)
	}
	if !got[0].LastUsed.IsZero() {
		t.Errorf("a passkey never used was used at %v", got[0].LastUsed)
	}

	phone.Credential, phone.LastUsed = []byte{9}, passkeyAt.Add(2*time.Hour)
	if err := s.PutPasskey(t.Context(), phone); err != nil {
		t.Fatalf("PutPasskey again: %v", err)
	}
	got = passkeysOf(t, s)
	if len(got) != 2 || !samePasskey(got[0], phone) {
		t.Errorf("Passkeys after a sign-in = %+v, want the phone replaced", got)
	}
}

func passkeysDelete(t *testing.T, s db.Repo) {
	for _, id := range [][]byte{{1}, {2}} {
		if err := s.PutPasskey(t.Context(), db.Passkey{ID: id, RPID: "cloud.example", Name: "key", Credential: []byte{1}, Created: passkeyAt}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeletePasskey(t.Context(), []byte{1}); err != nil {
		t.Fatalf("DeletePasskey: %v", err)
	}
	if got := passkeysOf(t, s); len(got) != 1 || !bytes.Equal(got[0].ID, []byte{2}) {
		t.Errorf("Passkeys after forgetting one = %+v", got)
	}
	if err := s.DeletePasskey(t.Context(), []byte{1}); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("forgetting it again = %v, want ErrNotFound", err)
	}
}
