// Package testdb gives each test its own empty PostgreSQL database.
//
// Set MIMIR_TEST_DATABASE_URL to a server whose role may create databases,
// such as postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable.
// Without it, tests that need a database are skipped.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Env names the variable holding the server tests create databases on.
const Env = "MIMIR_TEST_DATABASE_URL"

// URL creates a fresh database for the test and returns its URL. The
// database is dropped when the test ends, closing any connection left open.
// The test is skipped when MIMIR_TEST_DATABASE_URL is unset.
func URL(t testing.TB) string {
	t.Helper()
	admin := os.Getenv(Env)
	if admin == "" {
		t.Skip(Env + " is unset; set it to a PostgreSQL URL whose role may CREATE DATABASE, such as postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable")
	}
	u, err := url.Parse(admin)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatalf("%s must be a postgres:// URL", Env)
	}
	var b [8]byte
	rand.Read(b[:])
	name := "mimir_test_" + hex.EncodeToString(b[:])

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("testdb: connect to %s: %v", Env, err)
	}
	defer conn.Close(context.Background())
	// The name is ours, made of [a-z0-9_]; identifiers can't be parameters.
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("testdb: create database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, admin)
		if err != nil {
			t.Errorf("testdb: connect to drop %s: %v", name, err)
			return
		}
		defer conn.Close(context.Background())
		if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("testdb: drop %s: %v", name, err)
		}
	})
	u.Path = "/" + name
	return u.String()
}
