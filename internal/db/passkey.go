package db

import (
	"context"
	"time"
)

// Passkeys are the WebAuthn credentials that sign in to the web UI (#532),
// one row each. What a credential is -- its key, its counter, its flags -- is
// the WebAuthn library's, kept here as the bytes it encodes itself to, so a
// new version of the library is not a migration.
//
// No owner, as settings have none: Stratus has one user for good.
type Passkeys interface {
	// Passkeys is every credential, oldest first.
	Passkeys(ctx context.Context) ([]Passkey, error)

	// PutPasskey stores a credential, replacing the one with its ID: a new
	// one, and the same one after a sign-in moved its counter.
	PutPasskey(ctx context.Context, p Passkey) error

	// DeletePasskey forgets one, or returns ErrNotFound.
	DeletePasskey(ctx context.Context, id []byte) error
}

// Passkey is one credential.
type Passkey struct {
	// ID is the credential's own, as the authenticator made it.
	ID []byte
	// RPID is the host it was made for, which is the only one a browser will
	// use it on.
	RPID string
	// Name is what the settings page calls it.
	Name string
	// Credential is the library's encoding of everything else.
	Credential []byte
	// Created is when it was added, and LastUsed the last sign-in with it,
	// zero for none.
	Created, LastUsed time.Time
}
