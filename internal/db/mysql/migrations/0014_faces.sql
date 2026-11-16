-- The faces found in photographs and the people they are grouped into (#519).
-- See the SQLite migration of the same number.
CREATE TABLE face_scans (
    file_id  BIGINT        NOT NULL PRIMARY KEY,
    state    INT           NOT NULL,
    model    VARCHAR(128)  NOT NULL,
    blob_key VARCHAR(1024) NOT NULL,
    at       BIGINT        NOT NULL,
    CONSTRAINT face_scans_media FOREIGN KEY (file_id) REFERENCES media (file_id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_bin;

CREATE TABLE people (
    id   BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(255) NOT NULL DEFAULT ''
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;

CREATE TABLE faces (
    id        BIGINT        NOT NULL AUTO_INCREMENT PRIMARY KEY,
    file_id   BIGINT        NOT NULL,
    blob_key  VARCHAR(1024) NOT NULL,
    x         FLOAT         NOT NULL,
    y         FLOAT         NOT NULL,
    w         FLOAT         NOT NULL,
    h         FLOAT         NOT NULL,
    score     FLOAT         NOT NULL,
    vector    BLOB          NOT NULL,
    person_id BIGINT        NULL,
    INDEX faces_file (file_id),
    INDEX faces_person (person_id),
    CONSTRAINT faces_media FOREIGN KEY (file_id) REFERENCES media (file_id) ON DELETE CASCADE,
    CONSTRAINT faces_person FOREIGN KEY (person_id) REFERENCES people (id) ON DELETE SET NULL
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_bin;
