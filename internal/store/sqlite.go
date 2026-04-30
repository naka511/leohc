package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

// SQLiteStore provides persistence for tokens and refresh profiles.
type SQLiteStore struct {
	mu     sync.Mutex
	db     *sql.DB
	DBPath string
}

// NewSQLiteStore opens (or creates) a SQLite database.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir for db: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	s := &SQLiteStore{db: db, DBPath: dbPath}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS tokens (
			id TEXT PRIMARY KEY,
			data TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS refresh_profiles (
			id TEXT PRIMARY KEY,
			data TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// Close closes the database connection.
func (s *SQLiteStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// --------------- Tokens ---------------

// LoadTokens reads all tokens from the database.
func (s *SQLiteStore) LoadTokens() ([]map[string]interface{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query("SELECT data FROM tokens ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []map[string]interface{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var item map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			continue
		}
		tokens = append(tokens, item)
	}
	return tokens, nil
}

// ReplaceTokens atomically replaces all tokens.
func (s *SQLiteStore) ReplaceTokens(tokens []map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM tokens"); err != nil {
		return err
	}
	stmt, err := tx.Prepare("INSERT OR REPLACE INTO tokens (id, data) VALUES (?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, token := range tokens {
		id, _ := token["id"].(string)
		if id == "" {
			continue
		}
		raw, err := json.Marshal(token)
		if err != nil {
			continue
		}
		if _, err := stmt.Exec(id, string(raw)); err != nil {
			continue
		}
	}
	return tx.Commit()
}

// --------------- Refresh Profiles ---------------

// LoadRefreshProfiles reads all refresh profiles from the database.
func (s *SQLiteStore) LoadRefreshProfiles() ([]map[string]interface{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query("SELECT data FROM refresh_profiles ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var profiles []map[string]interface{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var item map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			continue
		}
		profiles = append(profiles, item)
	}
	return profiles, nil
}

// ReplaceRefreshProfiles atomically replaces all refresh profiles.
func (s *SQLiteStore) ReplaceRefreshProfiles(profiles []map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM refresh_profiles"); err != nil {
		return err
	}
	stmt, err := tx.Prepare("INSERT OR REPLACE INTO refresh_profiles (id, data) VALUES (?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, profile := range profiles {
		id, _ := profile["id"].(string)
		if id == "" {
			continue
		}
		raw, err := json.Marshal(profile)
		if err != nil {
			continue
		}
		if _, err := stmt.Exec(id, string(raw)); err != nil {
			continue
		}
	}
	return tx.Commit()
}
