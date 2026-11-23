-- The passkeys that sign in to the web UI (#532). The credential is the
-- WebAuthn library's own encoding of it, so a new version of the library is
-- not a migration. No owner, as settings have none. WebAuthn caps an ID at
-- 1023 bytes, inside what an InnoDB key may be.
CREATE TABLE passkeys (
    id           VARBINARY(1023) NOT NULL PRIMARY KEY,
    rp_id        VARCHAR(253)    NOT NULL,
    name         VARCHAR(255)    NOT NULL,
    credential   BLOB            NOT NULL,
    created_at   BIGINT          NOT NULL,
    last_used_at BIGINT          NULL
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_bin;
