package database

import (
	"database/sql"
	"fmt"
)

// openStdlib opens a database/sql handle via the pgx stdlib driver, which is
// what golang-migrate's postgres driver needs.
func openStdlib(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return db, nil
}
