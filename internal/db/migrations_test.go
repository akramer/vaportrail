package db

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestMigrations(t *testing.T) {
	// Create a temporary file for the database
	tmpfile, err := os.CreateTemp("", "testdb-*.db")
	if err != nil {
		t.Fatal(err)
	}
	dbPath := tmpfile.Name()
	tmpfile.Close()
	defer os.Remove(dbPath)

	// Initialize the database, which should run migrations
	db, err := New(dbPath)
	if err != nil {
		t.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	// Verify tables exist
	tables := []string{"targets", "results"}
	for _, table := range tables {
		var name string
		err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err == sql.ErrNoRows {
			t.Errorf("Table %s does not exist", table)
		} else if err != nil {
			t.Errorf("Failed to check for table %s: %v", table, err)
		}
	}

	// Verify indexes exist
	indexes := []string{"idx_results_time", "idx_results_target"}
	for _, index := range indexes {
		var name string
		err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='index' AND name=?", index).Scan(&name)
		if err == sql.ErrNoRows {
			t.Errorf("Index %s does not exist", index)
		} else if err != nil {
			t.Errorf("Failed to check for index %s: %v", index, err)
		}
	}
}
