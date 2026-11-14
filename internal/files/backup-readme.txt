Stratus database backups
========================

This folder holds backups of the Stratus server's database: the index that
gives every file in this cloud its name. The stored files cannot be found
without it, so it is worth keeping a copy of the newest one somewhere else
too.

Each backup is a SQLite database named by the UTC time it was taken. One is
taken every STRATUS_BACKUP_INTERVAL, a day by default, and the last seven are
kept; older ones are deleted on their own.

To change the folder or the interval, or to stop the backups with an interval
of 0, open Settings in the web UI's user menu, or set STRATUS_BACKUP_DIR and
STRATUS_BACKUP_INTERVAL in the server's environment, which wins over the page.
When the folder moves, the backups already taken stay where they were.

How to restore one:
https://github.com/stratus-private-cloud/stratus-backend#backups

This file is written when the folder is made, and deleting it is fine.
