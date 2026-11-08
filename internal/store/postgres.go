package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const columns = `code, user_id, destination_url, created_at, click_count`
const queryTimeout = 5 * time.Second

type Postgres struct{ Pool *pgxpool.Pool }

func (p *Postgres) Initialize(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := p.Pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS links (
 code text PRIMARY KEY, user_id text NOT NULL, destination_url text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 click_count bigint NOT NULL DEFAULT 0 CHECK (click_count >= 0))`)
	if err != nil {
		return fmt.Errorf("create links: %w", err)
	}
	for _, q := range []string{
		`CREATE INDEX IF NOT EXISTS links_user_created_idx ON links (user_id, created_at DESC, code)`,
		`CREATE INDEX IF NOT EXISTS links_popular_idx ON links (click_count DESC, code)`,
	} {
		if _, err = p.Pool.Exec(ctx, q); err != nil {
			return fmt.Errorf("create index: %w", err)
		}
	}
	return nil
}

// postgresError classifies transport and explicit server availability failures,
// while leaving SQL and application errors intact. Preserve the cause.
func postgresError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if strings.HasPrefix(pgErr.Code, "08") || pgErr.Code == "57P01" || pgErr.Code == "57P02" || pgErr.Code == "57P03" || pgErr.Code == "53300" {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return err
	}
	var connectErr *pgconn.ConnectError
	var netErr net.Error
	if errors.As(err, &connectErr) || errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return err
}

func scan(row pgx.Row) (Link, error) {
	var link Link
	err := row.Scan(&link.Code, &link.UserID, &link.DestinationURL, &link.CreatedAt, &link.ClickCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return Link{}, ErrNotFound
	}
	return link, postgresError(err)
}
func (p *Postgres) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	return postgresError(p.Pool.Ping(ctx))
}
func (p *Postgres) Create(ctx context.Context, code, user, destination string) (Link, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	return scan(p.Pool.QueryRow(ctx, `INSERT INTO links (code, user_id, destination_url) VALUES ($1,$2,$3) RETURNING `+columns, code, user, destination))
}
func (p *Postgres) Get(ctx context.Context, code string) (Link, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	return scan(p.Pool.QueryRow(ctx, `SELECT `+columns+` FROM links WHERE code=$1`, code))
}
func (p *Postgres) Click(ctx context.Context, code string) (Link, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	return scan(p.Pool.QueryRow(ctx, `UPDATE links SET click_count=click_count+1 WHERE code=$1 RETURNING `+columns, code))
}
func (p *Postgres) List(ctx context.Context, user string, limit int) ([]Link, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	return p.list(ctx, `SELECT `+columns+` FROM links WHERE user_id=$1 ORDER BY created_at DESC, code LIMIT $2`, user, limit)
}
func (p *Postgres) Popular(ctx context.Context, limit int) ([]Link, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	return p.list(ctx, `SELECT `+columns+` FROM links ORDER BY click_count DESC, code LIMIT $1`, limit)
}
func (p *Postgres) list(ctx context.Context, q string, args ...any) ([]Link, error) {
	rows, err := p.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, postgresError(err)
	}
	defer rows.Close()
	links := make([]Link, 0)
	for rows.Next() {
		link, err := scan(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, postgresError(rows.Err())
}
