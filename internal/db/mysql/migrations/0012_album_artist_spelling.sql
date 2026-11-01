-- One spelling per album artist. An artist is a GROUP BY over album_artist, so
-- tags that differ only in case -- "RJD2" and "Rjd2" -- were two artists, and
-- an album split between them two albums. PutMedia now gives a track the
-- spelling the library already has, and this gives the rows already written
-- the one most of their tracks carry, the same rule. Matched on
-- search_album_artist, which Go folded, so the three engines agree on what is
-- the same name. Stars and lookups kept under a spelling that loses are left
-- behind, as a retag leaves them.
--
-- A derived table, because MySQL refuses a subquery naming the table an UPDATE
-- writes.
UPDATE media m
JOIN (
    SELECT search_album_artist, album_artist FROM (
        SELECT search_album_artist, album_artist,
               ROW_NUMBER() OVER (PARTITION BY search_album_artist
                                  ORDER BY COUNT(*) DESC, album_artist) AS n
        FROM media
        WHERE kind = 'audio' AND album_artist <> ''
        GROUP BY search_album_artist, album_artist
    ) ranked WHERE n = 1
) chosen ON chosen.search_album_artist = m.search_album_artist
SET m.album_artist = chosen.album_artist
WHERE m.kind = 'audio' AND m.album_artist <> chosen.album_artist;
