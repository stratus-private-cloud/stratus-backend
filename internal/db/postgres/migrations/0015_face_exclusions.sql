-- A face taken out of the person it was grouped under (#525).
-- See the SQLite migration of the same number.
CREATE TABLE face_exclusions (
    face_id   BIGINT NOT NULL REFERENCES faces (id) ON DELETE CASCADE,
    person_id BIGINT NOT NULL REFERENCES people (id) ON DELETE CASCADE,
    PRIMARY KEY (face_id, person_id)
);

CREATE INDEX face_exclusions_person ON face_exclusions (person_id);
