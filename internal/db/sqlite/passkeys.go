package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/db/sqlutil"
)

// Passkeys implements db.Passkeys.
func (r *repo) Passkeys(ctx context.Context) ([]db.Passkey, error) {
	const query = `SELECT id, rp_id, name, credential, created_at, last_used_at
		FROM passkeys ORDER BY created_at, id`
	out, err := sqlutil.Collect(ctx, r.q, func(rows *sql.Rows) (db.Passkey, error) {
		var p db.Passkey
		err := rows.Scan(&p.ID, &p.RPID, &p.Name, &p.Credential, times.Dest(&p.Created), times.NullDest(&p.LastUsed))
		return p, err
	}, query)
	if err != nil {
		return nil, fmt.Errorf("read passkeys: %w", mapErr(err))
	}
	return out, nil
}

// PutPasskey implements db.Passkeys.
func (r *repo) PutPasskey(ctx context.Context, p db.Passkey) error {
	const query = `INSERT INTO passkeys (id, rp_id, name, credential, created_at, last_used_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET rp_id = excluded.rp_id, name = excluded.name,
			credential = excluded.credential, created_at = excluded.created_at, last_used_at = excluded.last_used_at`
	_, err := r.q.ExecContext(ctx, query, p.ID, p.RPID, p.Name, p.Credential, times.Arg(p.Created), times.NullArg(p.LastUsed))
	if err != nil {
		return fmt.Errorf("store a passkey: %w", mapErr(err))
	}
	return nil
}

// DeletePasskey implements db.Passkeys.
func (r *repo) DeletePasskey(ctx context.Context, id []byte) error {
	res, err := r.q.ExecContext(ctx, `DELETE FROM passkeys WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("forget a passkey: %w", mapErr(err))
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("forget a passkey: %w", mapErr(err))
	}
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}
