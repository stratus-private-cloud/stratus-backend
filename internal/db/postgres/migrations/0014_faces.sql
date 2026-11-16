-- The faces found in photographs and the people they are grouped into (#519).
-- See the SQLite migration of the same number.
CREATE TABLE face_scans (
    file_id  BIGINT      PRIMARY KEY REFERENCES media (file_id) ON DELETE CASCADE,
    state    INTEGER     NOT NULL,
    model    TEXT        NOT NULL,
    blob_key TEXT        NOT NULL,
    at       TIMESTAMPTZ NOT NULL
);

CREATE TABLE people (
    id   BIGSERIAL PRIMARY KEY,
    name TEXT      NOT NULL DEFAULT ''
);

CREATE TABLE faces (
    id        BIGSERIAL PRIMARY KEY,
    file_id   BIGINT    NOT NULL REFERENCES media (file_id) ON DELETE CASCADE,
    blob_key  TEXT      NOT NULL,
    x         REAL      NOT NULL,
    y         REAL      NOT NULL,
    w         REAL      NOT NULL,
    h         REAL      NOT NULL,
    score     REAL      NOT NULL,
    vector    BYTEA     NOT NULL,
    person_id BIGINT    REFERENCES people (id) ON DELETE SET NULL
);

CREATE INDEX faces_file ON faces (file_id);
CREATE INDEX faces_person ON faces (person_id);
