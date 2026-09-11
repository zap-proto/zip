// Command local-service is a zip service with one operation, backed by SQLite.
//
// The operation is declared once, in New, and every interface below is derived
// from that declaration:
//
//	go run . serve                          # REST, MCP and OpenAPI on 127.0.0.1:8080
//	go run . notes create --text "buy milk" # the same operation as a command
//	go run . openapi openapi.json           # the OpenAPI document, written to a file
//
// Notes are stored in notes.db in the working directory.
package main

//go:generate go tool zipdoc

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/hanzoai/sqlite"
	"github.com/zap-proto/zip"
)

// Note is one stored note.
type Note struct {
	// ID is the row id SQLite assigned.
	ID int64 `json:"id"`
	// Text is the note as written.
	Text string `json:"text"`
}

// AddIn is a note to store.
type AddIn struct {
	// Text is the note body.
	Text string `json:"text" validate:"required"`
}

// Store keeps notes in one SQLite file.
type Store struct {
	db *sql.DB
}

// Open opens the SQLite file at path, creating it and its table if needed.
func (s *Store) Open(path string) error {
	db, err := sqlite.OpenDB(path, nil)
	if err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS notes (id INTEGER PRIMARY KEY, text TEXT NOT NULL)`); err != nil {
		db.Close()
		return err
	}
	s.db = db
	return nil
}

// Add stores a note and returns it with its id.
//
// Example: {"text": "buy milk"}
// Response: {"id": 1, "text": "buy milk"}
func (s *Store) Add(ctx context.Context, in *AddIn) (*Note, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO notes (text) VALUES (?)`, in.Text)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &Note{ID: id, Text: in.Text}, nil
}

// New declares the service's operations. It does not touch the database, so the
// OpenAPI document can be written by a build step that has none.
func New(s *Store) *zip.App {
	app := zip.New(zip.Config{AppName: "notes"})
	zip.Post(app, "/v1/notes", s.Add)
	return app
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	s := &Store{}
	app := New(s)
	// `local-service openapi <file>` and `local-service declare <file>` write a
	// projection and stop here, before the database is opened.
	if done, err := app.Described(); done {
		return err
	}
	if err := s.Open("notes.db"); err != nil {
		return err
	}
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		return app.Listen("http://127.0.0.1:8080")
	}
	cli := app.CLI()
	cli.Out = os.Stdout
	return cli.Run(context.Background(), os.Args[1:])
}
