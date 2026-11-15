-- A photograph's vector, for searching by what is in it (#505). See the SQLite
-- migration of the same number for why it is a table of its own.
--
-- MySQL 9's VECTOR type would hold it, but the community server has no
-- distance function to search it with, so the search is in Go for all three.
CREATE TABLE embeddings (
    file_id  BIGINT        NOT NULL PRIMARY KEY,
    state    INT           NOT NULL,
    model    VARCHAR(128)  NOT NULL,
    blob_key VARCHAR(1024) NOT NULL,
    at       BIGINT        NOT NULL,
    vector   BLOB          NULL,
    CONSTRAINT embeddings_media FOREIGN KEY (file_id) REFERENCES media (file_id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_bin;
