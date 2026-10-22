package common

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/glebarez/go-sqlite" // registers the "sqlite" driver used below
)

// SQLITE_PATH is a plain file path in every real deployment, and the
// concurrency pragmas are a code-level correctness requirement rather than an
// operator preference: without WAL the writer blocks readers and vice versa,
// without busy_timeout a concurrent write surfaces as SQLITE_BUSY, and without
// _txlock=immediate a deferred transaction can die with SQLITE_BUSY_SNAPSHOT,
// which no busy timeout covers.
//
// The bug this pins: SQLITE_PATH used to REPLACE SQLitePath wholesale, so
// pointing the deployment at an absolute path silently dropped the concurrency
// parameters. The effective journal_mode fell back to SQLite's default
// (delete), and production logged 20 "database is locked" events over a few
// days before the regression was caught.
func TestBuildSQLiteDSNAppendsConcurrencyParams(t *testing.T) {
	dsn := BuildSQLiteDSN("/data/new-api/data/new-api.db")

	if !strings.HasPrefix(dsn, "/data/new-api/data/new-api.db?") {
		t.Fatalf("DSN must keep the operator's path as its base, got %q", dsn)
	}
	for _, want := range []string{"busy_timeout", "journal_mode(WAL)", "_txlock=immediate"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN %q is missing %q; a bare SQLITE_PATH must not drop the concurrency pragmas", dsn, want)
		}
	}
}

func TestBuildSQLiteDSNPreservesExistingParams(t *testing.T) {
	// An operator who already spelled out all three pragmas must get them back
	// unchanged: nothing may be overwritten or appended twice.
	dsn := BuildSQLiteDSN("/tmp/x.db?" + SQLiteConcurrencyParams)
	if got := strings.Count(dsn, "busy_timeout"); got != 1 {
		t.Errorf("busy_timeout appears %d times in %q; want exactly 1", got, dsn)
	}
	if got := strings.Count(dsn, "_txlock"); got != 1 {
		t.Errorf("_txlock appears %d times in %q; want exactly 1", got, dsn)
	}
}

func TestBuildSQLiteDSNDoesNotOverrideOperatorBusyTimeout(t *testing.T) {
	// Only busy_timeout given: the operator's value must be preserved, and the
	// missing journal_mode + _txlock must still be appended. The old `||`
	// implementation appended the whole parameter string, silently overriding
	// the operator's busy_timeout(1000) with busy_timeout(30000).
	dsn := BuildSQLiteDSN("/tmp/x.db?_pragma=busy_timeout(1000)")
	if got := strings.Count(dsn, "busy_timeout"); got != 1 {
		t.Fatalf("busy_timeout appears %d times in %q; want exactly 1 (operator value must not be overridden)", got, dsn)
	}
	if !strings.Contains(dsn, "_pragma=busy_timeout(1000)") {
		t.Errorf("DSN %q must keep the operator's busy_timeout(1000)", dsn)
	}
	if !strings.Contains(dsn, "journal_mode(WAL)") {
		t.Errorf("DSN %q is missing journal_mode(WAL)", dsn)
	}
	if !strings.Contains(dsn, "_txlock=immediate") {
		t.Errorf("DSN %q is missing _txlock=immediate", dsn)
	}
}

func TestBuildSQLiteDSNEnsuresTxlockWhenOnlyPragmasGiven(t *testing.T) {
	// busy_timeout + journal_mode given but _txlock absent: _txlock=immediate
	// must still be appended. The old `||` implementation returned the path
	// unchanged here, silently dropping _txlock.
	dsn := BuildSQLiteDSN("/tmp/x.db?_pragma=busy_timeout(1000)&_pragma=journal_mode(MEMORY)")
	if !strings.Contains(dsn, "_txlock=immediate") {
		t.Errorf("DSN %q is missing _txlock=immediate; it must be added when absent", dsn)
	}
	if !strings.Contains(dsn, "journal_mode(MEMORY)") {
		t.Errorf("DSN %q must keep the operator's journal_mode(MEMORY)", dsn)
	}
	if got := strings.Count(dsn, "busy_timeout"); got != 1 {
		t.Errorf("busy_timeout appears %d times in %q; want exactly 1", got, dsn)
	}
}

func TestBuildSQLiteDSNKeepsForeignParams(t *testing.T) {
	dsn := BuildSQLiteDSN("/tmp/x.db?cache=shared")
	if !strings.Contains(dsn, "cache=shared") {
		t.Errorf("DSN %q dropped the operator's own query parameters", dsn)
	}
	if !strings.Contains(dsn, "busy_timeout") {
		t.Errorf("DSN %q must still carry the concurrency pragmas", dsn)
	}
}

func TestBuildSQLiteDSNEmptyPathFallsBackToDefault(t *testing.T) {
	if got := BuildSQLiteDSN(""); got != SQLitePath {
		t.Errorf("empty path must fall back to the default DSN, got %q", got)
	}
}

func TestBuildSQLiteDSNIdempotent(t *testing.T) {
	once := BuildSQLiteDSN("/tmp/x.db?_pragma=busy_timeout(1000)&_pragma=journal_mode(WAL)&_txlock=immediate")
	twice := BuildSQLiteDSN(once)
	if once != twice {
		t.Errorf("BuildSQLiteDSN must be idempotent, got %q then %q", once, twice)
	}
}

// TestBuildSQLiteDSNPragmasTakeEffect opens a real database connection with the
// produced DSN and asks SQLite for the effective settings, so the test catches
// any regression in the pragma wiring itself (not just string shape).
func TestBuildSQLiteDSNPragmasTakeEffect(t *testing.T) {
	dsn := BuildSQLiteDSN(t.TempDir() + "/throwaway.db")

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	// Pin a single connection so we observe the pragmas set on connection open.
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("grab connection: %v", err)
	}
	defer conn.Close()

	var busyTimeout int
	if err := conn.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout != 30000 {
		t.Errorf("effective busy_timeout = %d, want 30000", busyTimeout)
	}

	var journalMode string
	if err := conn.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Errorf("effective journal_mode = %q, want wal", journalMode)
	}
}