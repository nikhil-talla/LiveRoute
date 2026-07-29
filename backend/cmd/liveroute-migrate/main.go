package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func run() error {
	var directory string
	flag.StringVar(&directory, "dir", "/app/migrations", "migration directory")
	flag.Parse()
	if flag.NArg() != 1 {
		return errors.New("usage: liveroute-migrate [-dir PATH] up|status|version")
	}
	databaseURL := os.Getenv("LIVEROUTE_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("LIVEROUTE_DATABASE_URL is required")
	}

	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()
	if err := database.Ping(); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}

	switch flag.Arg(0) {
	case "up":
		err = goose.Up(database, directory)
	case "status":
		err = goose.Status(database, directory)
	case "version":
		err = goose.Version(database, directory)
	default:
		return errors.New("migration command must be up, status, or version")
	}
	if err != nil {
		return fmt.Errorf("migration %s: %w", flag.Arg(0), err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}
}
