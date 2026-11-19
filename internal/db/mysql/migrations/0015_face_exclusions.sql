-- A face taken out of the person it was grouped under (#525).
-- See the SQLite migration of the same number.
CREATE TABLE face_exclusions (
    face_id   BIGINT NOT NULL,
    person_id BIGINT NOT NULL,
    PRIMARY KEY (face_id, person_id),
    INDEX face_exclusions_person (person_id),
    CONSTRAINT face_exclusions_face FOREIGN KEY (face_id) REFERENCES faces (id) ON DELETE CASCADE,
    CONSTRAINT face_exclusions_person FOREIGN KEY (person_id) REFERENCES people (id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_bin;
