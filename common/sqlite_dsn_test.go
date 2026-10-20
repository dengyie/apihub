package common

import (
	"strings"
	"testing"
)

// SQLITE_PATH is a plain file path in every real deployment, and the
// concurrency pragmas are a code-level correctness requirement rather than an
// operator preference: without busy_timeout a concurrent write fails instantly
// with SQLITE_BUSY, and without _txlock=immediate a deferred transaction can
// die with SQLITE_BUSY_SNAPSHOT, which no busy timeout covers.
//
// The bug this pins: SQLITE_PATH used to REPLACE SQLitePath wholesale, so
// pointing the deployment at an absolute path silently dropped all three
// parameters. Production ran the whole day on busy_timeout=0 and lost 17
// billing/log/quota writes to "database is locked" before anyone noticed.
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
	// An operator who already spelled out the pragmas must not get them twice:
	// duplicate _pragma entries are harmless but make the DSN unreadable in
	// logs, and the second copy of _txlock=immediate would be parsed as a
	// value rather than a flag.
	dsn := BuildSQLiteDSN("/tmp/x.db?" + SQLiteConcurrencyParams)
	if got := strings.Count(dsn, "busy_timeout"); got != 1 {
		t.Errorf("busy_timeout appears %d times in %q; want exactly 1", got, dsn)
	}
	if got := strings.Count(dsn, "_txlock"); got != 1 {
		t.Errorf("_txlock appears %d times in %q; want exactly 1", got, dsn)
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
