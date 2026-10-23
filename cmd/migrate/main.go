package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"tablekit.de/tablekit/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 {
		return errors.New("usage: migrate <up|down|status|version>")
	}
	dsn := os.Getenv("TABLEKIT_MIGRATE_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TABLEKIT_DATABASE_URL")
	}
	if dsn == "" {
		return errors.New("TABLEKIT_MIGRATE_DATABASE_URL or TABLEKIT_DATABASE_URL is required")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return err
	}

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}

	cmd := os.Args[1]
	switch cmd {
	case "up":
		return goose.Up(db, ".")
	case "down":
		return goose.Down(db, ".")
	case "status":
		return goose.Status(db, ".")
	case "version":
		return goose.Version(db, ".")
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}
