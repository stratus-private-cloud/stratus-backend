package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/db/sqlutil"
)

// faceScanWanted is pendingEmbeddings' condition over face_scans.
var faceScanWanted = `(s.file_id IS NULL OR s.model <> ? OR s.blob_key <> f.blob_key
		OR (s.state = ` + embeddingRefused + ` AND s.at <= ?))`

// currentFaces is every face of bytes its file still points at.
const currentFaces = `faces fa JOIN files f ON f.id = fa.file_id AND f.blob_key = fa.blob_key
		JOIN media m ON m.file_id = fa.file_id`

// PendingFaceScans implements db.Faces.
func (r *repo) PendingFaceScans(ctx context.Context, model string, staleBefore time.Time, limit int) ([]db.File, error) {
	query := `SELECT ` + joinedFileColumns + `
		FROM media m JOIN files f ON f.id = m.file_id
		LEFT JOIN face_scans s ON s.file_id = m.file_id
		WHERE m.kind = ` + kindImage + ` AND ` + faceScanWanted + `
		ORDER BY m.sort_at DESC, m.file_id DESC LIMIT ?`
	out, err := sqlutil.Collect(ctx, r.q, scanFileRow, query, model, times.Arg(staleBefore), limit)
	if err != nil {
		return nil, fmt.Errorf("list pending face scans: %w", mapErr(err))
	}
	return out, nil
}

// WantsFaceScan implements db.Faces.
func (r *repo) WantsFaceScan(ctx context.Context, fileID int64, model string, staleBefore time.Time) (bool, error) {
	query := `SELECT COUNT(*)
		FROM media m JOIN files f ON f.id = m.file_id
		LEFT JOIN face_scans s ON s.file_id = m.file_id
		WHERE m.file_id = ? AND m.kind = ` + kindImage + ` AND ` + faceScanWanted
	var n int
	if err := r.q.QueryRowContext(ctx, query, fileID, model, times.Arg(staleBefore)).Scan(&n); err != nil {
		return false, fmt.Errorf("ask whether file %d wants a face scan: %w", fileID, mapErr(err))
	}
	return n > 0, nil
}

// PutFaceScan implements db.Faces.
func (r *repo) PutFaceScan(ctx context.Context, s db.FaceScan) error {
	const query = `INSERT INTO face_scans (file_id, state, model, blob_key, at) VALUES (?, ?, ?, ?, ?)
		AS new ON DUPLICATE KEY UPDATE state = new.state, model = new.model,
			blob_key = new.blob_key, at = new.at`
	if _, err := r.q.ExecContext(ctx, query, s.FileID, int(s.State), s.Model, s.BlobKey, times.Arg(s.At)); err != nil {
		return fmt.Errorf("record face scan of file %d: %w", s.FileID, mapErr(err))
	}
	return nil
}

// AddFace implements db.Faces.
func (r *repo) AddFace(ctx context.Context, f db.Face) (int64, error) {
	const query = `INSERT INTO faces (file_id, blob_key, x, y, w, h, score, vector, person_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, 0))`
	result, err := r.q.ExecContext(ctx, query, f.FileID, f.BlobKey, f.X, f.Y, f.W, f.H, f.Score, f.Vector, f.Person)
	if err != nil {
		return 0, fmt.Errorf("record a face of file %d: %w", f.FileID, mapErr(err))
	}
	return result.LastInsertId()
}

func scanFace(rows *sql.Rows) (db.Face, error) { return sqlutil.ScanFace(rows, times) }

func scanFaceVector(rows *sql.Rows) (db.Face, error) { return sqlutil.ScanFaceVector(rows, times) }

// FacesOf implements db.Faces.
func (r *repo) FacesOf(ctx context.Context, fileID int64) ([]db.Face, error) {
	query := `SELECT ` + sqlutil.FaceColumns + ` FROM faces fa JOIN media m ON m.file_id = fa.file_id
		WHERE fa.file_id = ? ORDER BY fa.id`
	out, err := sqlutil.Collect(ctx, r.q, scanFace, query, fileID)
	if err != nil {
		return nil, fmt.Errorf("read the faces of file %d: %w", fileID, mapErr(err))
	}
	return out, nil
}

// DeleteFacesOf implements db.Faces.
func (r *repo) DeleteFacesOf(ctx context.Context, fileID int64) error {
	if _, err := r.q.ExecContext(ctx, `DELETE FROM faces WHERE file_id = ?`, fileID); err != nil {
		return fmt.Errorf("delete the faces of file %d: %w", fileID, mapErr(err))
	}
	return nil
}

// Face implements db.Faces.
func (r *repo) Face(ctx context.Context, id int64) (db.Face, error) {
	query := `SELECT ` + sqlutil.FaceColumns + ` FROM faces fa JOIN media m ON m.file_id = fa.file_id WHERE fa.id = ?`
	f, err := sqlutil.ScanFace(r.q.QueryRowContext(ctx, query, id), times)
	if err != nil {
		return db.Face{}, fmt.Errorf("read face %d: %w", id, mapErr(err))
	}
	return f, nil
}

// EachFace implements db.Faces.
func (r *repo) EachFace(ctx context.Context, model string, fn func(db.Face) error) error {
	query := `SELECT ` + sqlutil.FaceVectorColumns + ` FROM ` + currentFaces + `
		JOIN face_scans s ON s.file_id = fa.file_id AND s.blob_key = fa.blob_key
		WHERE s.model = ? ORDER BY fa.id`
	for f, err := range sqlutil.Seq(ctx, r.q, scanFaceVector, query, model) {
		if err != nil {
			return fmt.Errorf("read faces: %w", mapErr(err))
		}
		if err := fn(f); err != nil {
			return err
		}
	}
	return nil
}

// EachFaceCrop implements db.Faces.
func (r *repo) EachFaceCrop(ctx context.Context, fn func(id int64, blobKey string) error) error {
	type crop struct {
		id  int64
		key string
	}
	scan := func(rows *sql.Rows) (crop, error) {
		var c crop
		return c, rows.Scan(&c.id, &c.key)
	}
	for c, err := range sqlutil.Seq(ctx, r.q, scan, `SELECT id, blob_key FROM faces ORDER BY id`) {
		if err != nil {
			return fmt.Errorf("read faces: %w", mapErr(err))
		}
		if err := fn(c.id, c.key); err != nil {
			return err
		}
	}
	return nil
}

// PersonFaces implements db.Faces.
func (r *repo) PersonFaces(ctx context.Context, person int64, after db.FaceCursor, limit int) ([]db.Face, error) {
	query := `SELECT ` + sqlutil.FaceColumns + ` FROM ` + currentFaces + ` WHERE fa.person_id = ?`
	args := []any{person}
	if after.ID != 0 {
		query += ` AND (m.sort_at < ? OR (m.sort_at = ? AND fa.id < ?))`
		args = append(args, times.Arg(after.SortAt), times.Arg(after.SortAt), after.ID)
	}
	query += ` ORDER BY m.sort_at DESC, fa.id DESC LIMIT ?`
	out, err := sqlutil.Collect(ctx, r.q, scanFace, query, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("read the faces of person %d: %w", person, mapErr(err))
	}
	return out, nil
}

// personColumns is a person's row with what People says about them, over
// people as p.
const personColumns = `p.id, p.name,
	(SELECT COUNT(*) FROM ` + currentFaces + ` WHERE fa.person_id = p.id),
	COALESCE((SELECT fa.id FROM ` + currentFaces + ` WHERE fa.person_id = p.id
		ORDER BY fa.score DESC, fa.id LIMIT 1), 0)`

// People implements db.Faces.
func (r *repo) People(ctx context.Context) ([]db.Person, error) {
	out, err := sqlutil.Collect(ctx, r.q, func(rows *sql.Rows) (db.Person, error) { return sqlutil.ScanPerson(rows) },
		`SELECT `+personColumns+` FROM people p`)
	if err != nil {
		return nil, fmt.Errorf("list people: %w", mapErr(err))
	}
	return db.SortPeople(out), nil
}

// Person implements db.Faces.
func (r *repo) Person(ctx context.Context, id int64) (db.Person, error) {
	p, err := sqlutil.ScanPerson(r.q.QueryRowContext(ctx, `SELECT `+personColumns+` FROM people p WHERE p.id = ?`, id))
	if err != nil {
		return db.Person{}, fmt.Errorf("read person %d: %w", id, mapErr(err))
	}
	return p, nil
}

// NewPerson implements db.Faces.
func (r *repo) NewPerson(ctx context.Context, name string) (int64, error) {
	result, err := r.q.ExecContext(ctx, `INSERT INTO people (name) VALUES (?)`, name)
	if err != nil {
		return 0, fmt.Errorf("record a person: %w", mapErr(err))
	}
	return result.LastInsertId()
}

// AssignFaces implements db.Faces.
func (r *repo) AssignFaces(ctx context.Context, person int64, faces []int64) error {
	if len(faces) == 0 {
		return nil
	}
	query := `UPDATE faces SET person_id = ? WHERE person_id IS NULL
		AND NOT EXISTS (SELECT 1 FROM face_exclusions x WHERE x.face_id = faces.id AND x.person_id = ?)
		AND id IN (?` + strings.Repeat(", ?", len(faces)-1) + `)`
	args := make([]any, 0, len(faces)+2)
	args = append(args, person, person)
	for _, id := range faces {
		args = append(args, id)
	}
	if _, err := r.q.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("assign faces to person %d: %w", person, mapErr(err))
	}
	return nil
}

// RemoveFaces implements db.Faces.
func (r *repo) RemoveFaces(ctx context.Context, person int64, faces []int64) error {
	if len(faces) == 0 {
		return nil
	}
	ids := `id IN (?` + strings.Repeat(", ?", len(faces)-1) + `)`
	args := make([]any, 0, len(faces)+1)
	args = append(args, person)
	for _, id := range faces {
		args = append(args, id)
	}
	if _, err := r.q.ExecContext(ctx, `INSERT IGNORE INTO face_exclusions (face_id, person_id)
		SELECT id, person_id FROM faces WHERE person_id = ? AND `+ids, args...); err != nil {
		return fmt.Errorf("keep faces out of person %d: %w", person, mapErr(err))
	}
	if _, err := r.q.ExecContext(ctx, `UPDATE faces SET person_id = NULL WHERE person_id = ? AND `+ids, args...); err != nil {
		return fmt.Errorf("take faces out of person %d: %w", person, mapErr(err))
	}
	return nil
}

// EachFaceExclusion implements db.Faces.
func (r *repo) EachFaceExclusion(ctx context.Context, fn func(face, person int64) error) error {
	type exclusion struct{ face, person int64 }
	scan := func(rows *sql.Rows) (exclusion, error) {
		var e exclusion
		return e, rows.Scan(&e.face, &e.person)
	}
	query := `SELECT face_id, person_id FROM face_exclusions ORDER BY face_id, person_id`
	for e, err := range sqlutil.Seq(ctx, r.q, scan, query) {
		if err != nil {
			return fmt.Errorf("read face exclusions: %w", mapErr(err))
		}
		if err := fn(e.face, e.person); err != nil {
			return err
		}
	}
	return nil
}

// RenamePerson implements db.Faces.
func (r *repo) RenamePerson(ctx context.Context, id int64, name string) error {
	result, err := r.q.ExecContext(ctx, `UPDATE people SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return fmt.Errorf("rename person %d: %w", id, mapErr(err))
	}
	return affectedOne(result, "rename person", id)
}

// MergePeople implements db.Faces.
func (r *repo) MergePeople(ctx context.Context, from, into int64) error {
	if _, err := r.Person(ctx, into); err != nil {
		return err
	}
	if _, err := r.q.ExecContext(ctx, `UPDATE faces SET person_id = ? WHERE person_id = ?`, into, from); err != nil {
		return fmt.Errorf("merge person %d into %d: %w", from, into, mapErr(err))
	}
	// What into already has stays behind, and goes with from below.
	if _, err := r.q.ExecContext(ctx, `UPDATE IGNORE face_exclusions SET person_id = ? WHERE person_id = ?`, into, from); err != nil {
		return fmt.Errorf("merge person %d into %d: %w", from, into, mapErr(err))
	}
	result, err := r.q.ExecContext(ctx, `DELETE FROM people WHERE id = ?`, from)
	if err != nil {
		return fmt.Errorf("merge person %d into %d: %w", from, into, mapErr(err))
	}
	return affectedOne(result, "merge person", from)
}

// DeleteEmptyPeople implements db.Faces.
func (r *repo) DeleteEmptyPeople(ctx context.Context) (int64, error) {
	result, err := r.q.ExecContext(ctx, `DELETE FROM people WHERE name = ''
		AND NOT EXISTS (SELECT 1 FROM faces fa WHERE fa.person_id = people.id)`)
	if err != nil {
		return 0, fmt.Errorf("delete people with no faces: %w", mapErr(err))
	}
	return result.RowsAffected()
}

// ForgetFaces implements db.Faces.
func (r *repo) ForgetFaces(ctx context.Context) error {
	for _, table := range []string{"face_exclusions", "faces", "people", "face_scans"} {
		if _, err := r.q.ExecContext(ctx, `DELETE FROM `+table); err != nil {
			return fmt.Errorf("forget faces: %w", mapErr(err))
		}
	}
	return nil
}

// FaceCounts implements db.Faces.
func (r *repo) FaceCounts(ctx context.Context, model string) (db.FaceCounts, error) {
	query := `SELECT COUNT(*),
			COUNT(CASE WHEN s.state = ` + embeddingMade + ` AND s.model = ? AND s.blob_key = f.blob_key THEN 1 END),
			COUNT(CASE WHEN s.state = ` + embeddingRefused + ` AND s.model = ? AND s.blob_key = f.blob_key THEN 1 END),
			(SELECT COUNT(*) FROM ` + currentFaces + `),
			(SELECT COUNT(*) FROM people p WHERE EXISTS (SELECT 1 FROM ` + currentFaces + ` WHERE fa.person_id = p.id))
		FROM media m JOIN files f ON f.id = m.file_id
		LEFT JOIN face_scans s ON s.file_id = m.file_id
		WHERE m.kind = ` + kindImage
	var c db.FaceCounts
	if err := r.q.QueryRowContext(ctx, query, model, model).Scan(&c.Files, &c.Scanned, &c.Refused, &c.Faces, &c.People); err != nil {
		return db.FaceCounts{}, fmt.Errorf("count faces: %w", mapErr(err))
	}
	return c, nil
}

func affectedOne(result sql.Result, what string, id int64) error {
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s %d: %w", what, id, err)
	}
	if n == 0 {
		return fmt.Errorf("%s %d: %w", what, id, db.ErrNotFound)
	}
	return nil
}
