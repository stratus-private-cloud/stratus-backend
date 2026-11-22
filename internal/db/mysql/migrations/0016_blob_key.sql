-- Which keys a file row still names (#553), asked by the sweep before it
-- puts an orphan in the trash and by the pass that empties the trash before
-- it destroys bytes. Without it each question is a scan of every file.
CREATE INDEX files_blob_key ON files (blob_key(191));
