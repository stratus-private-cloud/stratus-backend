-- The passkeys that sign in to the web UI (#532). The credential is the
-- WebAuthn library's own encoding of it, so a new version of the library is
-- not a migration. No owner, as settings have none.
CREATE TABLE passkeys (
    id           BYTEA       PRIMARY KEY,
    rp_id        TEXT        NOT NULL,
    name         TEXT        NOT NULL,
    credential   BYTEA       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ
);
