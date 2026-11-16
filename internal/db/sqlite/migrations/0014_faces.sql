-- The faces found in photographs and the people they are grouped into (#519).
--
-- face_scans is embeddings without the vector: no row is a photograph nobody
-- has looked at, state says it was looked at (1) or refused (2), and model and
-- blob_key say by what and in which bytes. faces is what was found, many to a
-- photograph, each with the blob key it was found in, so a face of bytes an
-- overwrite replaced can be told from a current one without another join.
-- The box is fractions of the photograph turned the right way up.
--
-- A person deleted leaves their faces to nobody rather than taking them, and a
-- file deleted takes its scan and its faces with its metadata.
CREATE TABLE face_scans (
    file_id  INTEGER PRIMARY KEY REFERENCES media (file_id) ON DELETE CASCADE,
    state    INTEGER NOT NULL,
    model    TEXT    NOT NULL,
    blob_key TEXT    NOT NULL,
    at       INTEGER NOT NULL
);

CREATE TABLE people (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE faces (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id   INTEGER NOT NULL REFERENCES media (file_id) ON DELETE CASCADE,
    blob_key  TEXT    NOT NULL,
    x         REAL    NOT NULL,
    y         REAL    NOT NULL,
    w         REAL    NOT NULL,
    h         REAL    NOT NULL,
    score     REAL    NOT NULL,
    vector    BLOB    NOT NULL,
    person_id INTEGER REFERENCES people (id) ON DELETE SET NULL
);

CREATE INDEX faces_file ON faces (file_id);
CREATE INDEX faces_person ON faces (person_id);
