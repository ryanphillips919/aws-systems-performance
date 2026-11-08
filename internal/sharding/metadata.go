package sharding

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// VerifyMetadata refuses to adopt a populated unmarked database or an existing
// database with a different placement or shard identity. No data is migrated.
func VerifyMetadata(ctx context.Context, pool *pgxpool.Pool, id string, p *Placement) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS shard_placement_metadata (singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), shard_id text NOT NULL, fingerprint text NOT NULL)`); err != nil {
		return err
	}
	// Serialize bootstrap and prevent concurrent link writes while checking emptiness.
	if _, err = tx.Exec(ctx, `LOCK TABLE shard_placement_metadata, links IN ACCESS EXCLUSIVE MODE`); err != nil {
		return err
	}
	var oldID, oldFingerprint string
	err = tx.QueryRow(ctx, `SELECT shard_id, fingerprint FROM shard_placement_metadata WHERE singleton=true`).Scan(&oldID, &oldFingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM links LIMIT 1)`).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("shard %s contains links without placement metadata; use fresh databases or migrate explicitly", id)
		}
		_, err = tx.Exec(ctx, `INSERT INTO shard_placement_metadata(singleton,shard_id,fingerprint) VALUES(true,$1,$2)`, id, p.Fingerprint())
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if oldID != id || oldFingerprint != p.Fingerprint() {
		return fmt.Errorf("shard %s placement metadata mismatch (stored shard=%s fingerprint=%s, configured fingerprint=%s); migration required", id, oldID, oldFingerprint, p.Fingerprint())
	}
	return tx.Commit(ctx)
}
