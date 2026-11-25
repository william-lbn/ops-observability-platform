package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestPostgresIsolationAndAtomicAudit(t *testing.T) {
	dsn := os.Getenv("OPS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("real PostgreSQL test requires OPS_TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, e := Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.DB.Close()
	schema := fmt.Sprintf("ops_test_%d", time.Now().UnixNano())
	role := schema + "_role"
	if _, e = admin.DB.ExecContext(ctx, `CREATE SCHEMA `+schema+`; CREATE ROLE `+role+` NOLOGIN NOSUPERUSER NOBYPASSRLS`); e != nil {
		t.Fatal(e)
	}
	defer func() { admin.DB.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE; DROP ROLE `+role) }()
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, e := Open(ctx, u.String())
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.ExecContext(ctx, `GRANT USAGE ON SCHEMA `+schema+` TO `+role+`; GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA `+schema+` TO `+role+`; GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA `+schema+` TO `+role); e != nil {
		t.Fatal(e)
	}
	insert := func(tenant string, rollback bool) error {
		return s.Tx(ctx, tenant, func(tx *sql.Tx) error {
			if _, e := tx.ExecContext(ctx, `SET LOCAL ROLE `+role); e != nil {
				return e
			}
			if e := Audit(ctx, tx, tenant, "tester", "configuration.changed", "example", "test-request", map[string]bool{"realDatabase": true}); e != nil {
				return e
			}
			if rollback {
				return fmt.Errorf("injected pre-commit failure")
			}
			return nil
		})
	}
	if e = insert("team-a", false); e != nil {
		t.Fatal(e)
	}
	if e = insert("team-a", true); e == nil {
		t.Fatal("injected rollback missing")
	}
	for _, tenant := range []string{"team-a", "team-b"} {
		e = s.Tx(ctx, tenant, func(tx *sql.Tx) error {
			if _, e := tx.ExecContext(ctx, `SET LOCAL ROLE `+role); e != nil {
				return e
			}
			var a, b int
			if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM audits`).Scan(&a); e != nil {
				return e
			}
			if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM outbox`).Scan(&b); e != nil {
				return e
			}
			want := 0
			if tenant == "team-a" {
				want = 1
			}
			if a != want || b != want {
				t.Fatalf("tenant %s: audits=%d outbox=%d want=%d", tenant, a, b, want)
			}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Tx(ctx, "team-b", func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `SET LOCAL ROLE `+role); e != nil {
			return e
		}
		return Audit(ctx, tx, "team-a", "tester", "forged", "x", "x", nil)
	}); e == nil {
		t.Fatal("RLS accepted forged tenant write")
	}
}
