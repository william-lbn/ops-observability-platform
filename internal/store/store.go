package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	_ "github.com/jackc/pgx/v5/stdlib"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct{ DB *sql.DB }

func Open(ctx context.Context, dsn string) (*Store, error) {
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(10 * time.Minute)
	if e = db.PingContext(ctx); e != nil {
		db.Close()
		return nil, e
	}
	return &Store{db}, nil
}
func (s *Store) Migrate(ctx context.Context) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(741852963)"); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version integer PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())`); e != nil {
		return e
	}
	entries, e := migrations.ReadDir("migrations")
	if e != nil {
		return e
	}
	for _, entry := range entries {
		version, e := strconv.Atoi(strings.SplitN(entry.Name(), "_", 2)[0])
		if e != nil {
			return e
		}
		var exists bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, version).Scan(&exists); e != nil {
			return e
		}
		if exists {
			continue
		}
		b, e := migrations.ReadFile("migrations/" + entry.Name())
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, string(b)); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES($1) ON CONFLICT DO NOTHING`, version); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Store) Tx(ctx context.Context, tenant string, fn func(*sql.Tx) error) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "SELECT set_config('app.tenant',$1,true)", tenant); e != nil {
		return e
	}
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit()
}
func Audit(ctx context.Context, tx *sql.Tx, tenant, actor, action, object, request string, detail any) error {
	b, e := json.Marshal(detail)
	if e != nil {
		return e
	}
	var seq int64
	e = tx.QueryRowContext(ctx, `INSERT INTO audits(tenant,actor,action,object_id,request_id,detail) VALUES($1,$2,$3,$4,$5,$6) RETURNING sequence`, tenant, actor, action, object, request, b).Scan(&seq)
	if e != nil {
		return e
	}
	msg, _ := json.Marshal(map[string]any{"sequence": fmt.Sprint(seq), "action": action, "object": object, "actor": actor})
	_, e = tx.ExecContext(ctx, `INSERT INTO outbox(tenant,audit_sequence,payload) VALUES($1,$2,$3)`, tenant, seq, msg)
	return e
}
