package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/db/sqlutil"
)

var (
	embeddingMade    = strconv.Itoa(int(db.EmbeddingMade))
	embeddingRefused = strconv.Itoa(int(db.EmbeddingRefused))
	kindImage        = `'` + string(db.KindImage) + `'`
)

// pendingEmbeddings is PendingThumbs' query for the third half: the same walk
// of media_kind_sort, with the state looked up per row in a table of its own.
var pendingEmbeddings = `SELECT ` + joinedFileColumns + `
		FROM media m JOIN files f ON f.id = m.file_id
		LEFT JOIN embeddings e ON e.file_id = m.file_id
		WHERE m.kind = ` + kindImage + `
			AND (e.file_id IS NULL OR e.model <> $1 OR e.blob_key <> f.blob_key
				OR (e.state = ` + embeddingRefused + ` AND e.at <= $2))
		ORDER BY m.sort_at DESC, m.file_id DESC LIMIT $3`

// PendingEmbeddings implements db.Embeddings.
func (r *repo) PendingEmbeddings(ctx context.Context, model string, staleBefore time.Time, limit int) ([]db.File, error) {
	out, err := sqlutil.Collect(ctx, r.q, scanFileRow, pendingEmbeddings, model, times.Arg(staleBefore), limit)
	if err != nil {
		return nil, fmt.Errorf("list pending embeddings: %w", mapErr(err))
	}
	return out, nil
}

// PutEmbedding implements db.Embeddings. The foreign key is what answers a
// file that went meanwhile.
func (r *repo) PutEmbedding(ctx context.Context, e db.Embedding) error {
	const query = `INSERT INTO embeddings (file_id, state, model, blob_key, at, vector) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (file_id) DO UPDATE SET state = excluded.state, model = excluded.model,
			blob_key = excluded.blob_key, at = excluded.at, vector = excluded.vector`
	if _, err := r.q.ExecContext(ctx, query, e.FileID, int(e.State), e.Model, e.BlobKey, times.Arg(e.At), e.Vector); err != nil {
		return fmt.Errorf("record embedding of file %d: %w", e.FileID, mapErr(err))
	}
	return nil
}

// EachEmbedding implements db.Embeddings.
func (r *repo) EachEmbedding(ctx context.Context, model string, fn func(fileID int64, vector []byte) error) error {
	query := `SELECT e.file_id, e.vector FROM embeddings e JOIN files f ON f.id = e.file_id
		WHERE e.state = ` + embeddingMade + ` AND e.model = $1 AND e.blob_key = f.blob_key`
	if err := sqlutil.EachVector(ctx, r.q, query, model, fn); err != nil {
		return fmt.Errorf("read embeddings: %w", mapErr(err))
	}
	return nil
}

// EmbeddingCounts implements db.Embeddings.
func (r *repo) EmbeddingCounts(ctx context.Context, model string) (db.EmbeddingCounts, error) {
	query := `SELECT COUNT(*),
			COUNT(CASE WHEN e.state = ` + embeddingMade + ` AND e.model = $1 AND e.blob_key = f.blob_key THEN 1 END),
			COUNT(CASE WHEN e.state = ` + embeddingRefused + ` AND e.model = $2 AND e.blob_key = f.blob_key THEN 1 END)
		FROM media m JOIN files f ON f.id = m.file_id
		LEFT JOIN embeddings e ON e.file_id = m.file_id
		WHERE m.kind = ` + kindImage

	var c db.EmbeddingCounts
	if err := r.q.QueryRowContext(ctx, query, model, model).Scan(&c.Files, &c.Made, &c.Refused); err != nil {
		return db.EmbeddingCounts{}, fmt.Errorf("count embeddings: %w", mapErr(err))
	}
	return c, nil
}

// FilesByID implements db.Files, built like MediaStates and for the same
// reason: the list is as long as a page of answers.
func (r *repo) FilesByID(ctx context.Context, owner string, ids []int64) ([]db.File, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var places strings.Builder
	args := make([]any, 0, len(ids)+1)
	args = append(args, owner)
	for i, id := range ids {
		if i > 0 {
			places.WriteString(", ")
		}
		places.WriteString("$" + strconv.Itoa(i+2))
		args = append(args, id)
	}
	query := `SELECT ` + fileColumns + ` FROM files WHERE owner_id = $1 AND id IN (` + places.String() + `)`
	out, err := sqlutil.Collect(ctx, r.q, scanFileRow, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read files by id: %w", mapErr(err))
	}
	return out, nil
}
