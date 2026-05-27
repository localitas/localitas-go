package client

import "time"

type Database struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	OwnerID   string            `json:"owner_id"`
	System    bool              `json:"system"`
	Variables map[string]string `json:"variables"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type Column struct {
	Name       string      `json:"name"`
	Type       string      `json:"type"`
	PrimaryKey bool        `json:"primary_key,omitempty"`
	NotNull    bool        `json:"not_null,omitempty"`
	Unique     bool        `json:"unique,omitempty"`
	Default    string      `json:"default,omitempty"`
	ForeignKey *ForeignKey `json:"foreign_key,omitempty"`
}

type ForeignKey struct {
	Table  string `json:"table"`
	Column string `json:"column"`
}

type Table struct {
	ID         string            `json:"id"`
	DatabaseID string            `json:"database_id"`
	Name       string            `json:"name"`
	Engine     string            `json:"engine"`
	Columns    []Column          `json:"columns"`
	Variables  map[string]string `json:"variables"`
	RowCount   int64             `json:"row_count"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

type Row struct {
	ID        string                 `json:"id"`
	TableID   string                 `json:"table_id"`
	Values    map[string]interface{} `json:"values"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
}

type DatabaseMigration struct {
	ID           string `json:"id"`
	DatabaseID   string `json:"database_id"`
	Version      string `json:"version"`
	Description  string `json:"description"`
	UpSQL        string `json:"up_sql"`
	DownSQL      string `json:"down_sql"`
	AppliedAt    int64  `json:"applied_at"`
	RolledBackAt *int64 `json:"rolled_back_at,omitempty"`
}

type SearchIndexEntry struct {
	ID         string `json:"id"`
	OwnerID    string `json:"owner_id"`
	DatabaseID string `json:"database_id"`
	TableID    string `json:"table_id"`
	RowID      string `json:"row_id"`
	Content    string `json:"content"`
	Embedding  []byte `json:"embedding,omitempty"`
	UpdatedAt  int64  `json:"updated_at"`
}

type SearchResponse struct {
	Mode    string             `json:"mode"`
	Results []SearchIndexEntry `json:"results"`
}

type SearchRowsResponse struct {
	Rows  []map[string]interface{} `json:"rows"`
	Total int64                    `json:"total"`
}

type ServiceEntry struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type SQLStatement struct {
	SQL  string        `json:"sql"`
	Args []interface{} `json:"args,omitempty"`
}

type SQLExecResult struct {
	LastInsertID int64 `json:"last_insert_id"`
	RowsAffected int64 `json:"rows_affected"`
}

type SQLQueryResult struct {
	Columns []string        `json:"columns"`
	Types   []string        `json:"types"`
	Rows    [][]interface{} `json:"rows"`
}
