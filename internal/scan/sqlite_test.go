package scan

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// buildDB creates a SQLite fixture at path by running stmts. Fixtures are
// built in tests, never checked in as binary files.
func buildDB(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
}

func TestOpenROCannotWrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.db")
	buildDB(t, p, `CREATE TABLE t (a TEXT)`, `INSERT INTO t VALUES ('x')`)
	db, err := openRO(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO t VALUES ('y')`); err == nil {
		t.Fatal("write succeeded on a read-only connection")
	}
	if !hasColumns(db, "t", "a") || hasColumns(db, "t", "b") || hasColumns(db, "missing") {
		t.Fatal("hasColumns wrong")
	}
}
