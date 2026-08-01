package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const developmentTokenSize = 43

type seedConfig struct {
	databaseURL string
	tokenPath   string
	userID      string
	displayName string
	timeZone    string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("admin command failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] != "seed-dev" {
		return errors.New("usage: liveroute-admin seed-dev")
	}
	flags := flag.NewFlagSet("seed-dev", flag.ContinueOnError)
	tokenPath := flags.String("token-path", "/run/secrets/liveroute_dev_token", "Docker secret path")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("seed-dev does not accept positional arguments")
	}
	config := seedConfig{
		databaseURL: os.Getenv("LIVEROUTE_DATABASE_URL"),
		tokenPath:   *tokenPath,
		userID:      os.Getenv("LIVEROUTE_DEV_USER_ID"),
		displayName: os.Getenv("LIVEROUTE_DEV_DISPLAY_NAME"),
		timeZone:    os.Getenv("LIVEROUTE_DEV_TIME_ZONE_NAME"),
	}
	if err := config.validate(); err != nil {
		return err
	}
	tokenDigest, err := readTokenDigest(config.tokenPath)
	if err != nil {
		return err
	}
	database, err := sql.Open("pgx", config.databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()
	if err := database.PingContext(context.Background()); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	if err := seedUser(context.Background(), database, config, tokenDigest); err != nil {
		return err
	}
	slog.Info("development user seeded", "user_id", config.userID)
	return nil
}

func (config seedConfig) validate() error {
	if config.databaseURL == "" || config.tokenPath == "" ||
		!canonicalUUID(config.userID) || config.displayName == "" || config.timeZone == "" {
		return errors.New("database URL, token path, user id, display name, and time zone are required")
	}
	return nil
}

func readTokenDigest(path string) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	raw, err := os.ReadFile(path)
	if err != nil {
		return digest, fmt.Errorf("read development token secret: %w", err)
	}
	if len(raw) != developmentTokenSize || !validDevelopmentToken(string(raw)) {
		return digest, errors.New("development token secret is not canonical")
	}
	return sha256.Sum256(raw), nil
}

func seedUser(ctx context.Context, database *sql.DB, config seedConfig, tokenDigest [sha256.Size]byte) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin seed transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO users (id, display_name, default_time_zone_name)
		VALUES ($1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET
		  display_name = EXCLUDED.display_name,
		  default_time_zone_name = EXCLUDED.default_time_zone_name
	`, config.userID, config.displayName, config.timeZone); err != nil {
		return fmt.Errorf("upsert development user: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		UPDATE development_auth_tokens
		SET revoked_at = clock_timestamp()
		WHERE user_id = $1 AND revoked_at IS NULL AND token_sha256 <> $2
	`, config.userID, tokenDigest[:]); err != nil {
		return fmt.Errorf("revoke previous development tokens: %w", err)
	}
	tokenID, err := newUUID()
	if err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO development_auth_tokens (id, user_id, token_sha256, expires_at, revoked_at)
		VALUES ($1, $2, $3, NULL, NULL)
		ON CONFLICT (token_sha256) DO UPDATE SET
		  user_id = EXCLUDED.user_id,
		  expires_at = NULL,
		  revoked_at = NULL
	`, tokenID, config.userID, tokenDigest[:]); err != nil {
		return fmt.Errorf("store development token digest: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit seed transaction: %w", err)
	}
	return nil
}

func validDevelopmentToken(token string) bool {
	if len(token) != developmentTokenSize {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == token
}

func canonicalUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || strings.ToLower(value) != value {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func newUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate token id: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}
