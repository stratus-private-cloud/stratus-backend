-- One spelling per album artist. An artist is a GROUP BY over album_artist, so
-- tags that differ only in case -- "RJD2" and "Rjd2" -- were two artists, and
-- an album split between them two albums. PutMedia now gives a track the
-- spelling the library already has, and this gives the rows already written
-- the one most of their tracks carry, the same rule. Matched on
-- search_album_artist, which Go folded, so the three engines agree on what is
-- the same name. Stars and lookups kept under a spelling that loses are left
-- behind, as a retag leaves them.
UPDATE media SET album_artist = (
    SELECT m2.album_artist FROM media m2
    WHERE m2.kind = 'audio' AND m2.search_album_artist = media.search_album_artist
    GROUP BY m2.album_artist
    ORDER BY COUNT(*) DESC, m2.album_artist
    LIMIT 1
)
WHERE kind = 'audio' AND album_artist <> '';
