package scan

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// openRO opens a SQLite database read-only. backecho never creates,
// migrates, or checkpoints a vendor database, and never takes a write lock.
// Callers copy what they need out and close the connection before render.
func openRO(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(2000)"}
	dsn := u.String()
	if !strings.HasPrefix(filepath.ToSlash(path), "/") {
		// Windows drive paths: file:C:/x.db
		dsn = "file:" + filepath.ToSlash(path) + "?" + u.RawQuery
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// columns returns the column names of a table, empty if it does not exist.
func columns(db *sql.DB, table string) map[string]bool {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			out[name] = true
		}
	}
	return out
}

// hasColumns reports whether table exists with every named column.
func hasColumns(db *sql.DB, table string, cols ...string) bool {
	have := columns(db, table)
	if len(have) == 0 {
		return false
	}
	for _, c := range cols {
		if !have[c] {
			return false
		}
	}
	return true
}
