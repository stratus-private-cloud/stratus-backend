-- A face taken out of the person it was grouped under (#525): one row per
-- face and person it is not, so grouping never puts it back there. A table
-- and not a column because a face can be taken out of somebody and then out
-- of whoever it joins next.
--
-- Both keys cascade: a face found again in new bytes is a new face, and a
-- person forgotten has nobody left to be kept out of.
CREATE TABLE face_exclusions (
    face_id   INTEGER NOT NULL REFERENCES faces (id) ON DELETE CASCADE,
    person_id INTEGER NOT NULL REFERENCES people (id) ON DELETE CASCADE,
    PRIMARY KEY (face_id, person_id)
);

CREATE INDEX face_exclusions_person ON face_exclusions (person_id);
