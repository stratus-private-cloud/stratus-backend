-- A photograph's vector, for searching by what is in it (#505). See the SQLite
-- migration of the same number for why it is a table of its own.
--
-- BYTEA and not pgvector: the extension is not in the stock image and needs a
-- superuser to install, and the other two engines have nothing like it, so the
-- search is in Go for all three.
CREATE TABLE embeddings (
    file_id  BIGINT      PRIMARY KEY REFERENCES media (file_id) ON DELETE CASCADE,
    state    INTEGER     NOT NULL,
    model    TEXT        NOT NULL,
    blob_key TEXT        NOT NULL,
    at       TIMESTAMPTZ NOT NULL,
    vector   BYTEA
);
