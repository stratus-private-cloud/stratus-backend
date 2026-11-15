-- A photograph's vector, for searching by what is in it (#505).
--
-- A table and not columns on media, unlike the thumbnail state: 772 bytes on a
-- row the indexer and the gallery walk for other reasons would be paid by every
-- one of those walks. The foreign key is on media rather than files, so the
-- vector goes with the metadata it is a third half of.
--
-- No row means nothing has read the photograph; state says it was read (1) or
-- refused (2), and a refusal comes back after a week, as a thumbnail's does.
-- model names the vector space, and blob_key the bytes it was read from.
--
-- No index beyond the key. The pending query walks media_kind_sort and looks
-- this up per row, and an index in memory is built from one pass over it.
CREATE TABLE embeddings (
    file_id  INTEGER PRIMARY KEY REFERENCES media (file_id) ON DELETE CASCADE,
    state    INTEGER NOT NULL,
    model    TEXT    NOT NULL,
    blob_key TEXT    NOT NULL,
    at       INTEGER NOT NULL,
    vector   BLOB
);
