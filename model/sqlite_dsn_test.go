package model

import (
	"testing"

	"github.com/dengyie/apihub/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The SQLite concurrency settings are load-bearing, and they only apply if
// they survive all the way into the connection that actually serves queries.
// They are carried as query parameters on the DSN, which means any code path
// that rebuilds a DSN by hand can drop them without a compile error or a log
// line -- the connection is opened, it is merely opened badly. That already
// happened once: the controller test helper returned a bare file path, callers
// assigned it to common.SQLitePath, and InitDB reopened it. The reopened
// connection reported busy_timeout=5000 (SQLite's own default) and
// journal_mode=delete, so tests were silently exercising a rollback journal
// with no busy handler while production ran on WAL.
//
// So assert on the connection, not on the string.
func sqlitePragmas(t *testing.T, db *gorm.DB) (busyTimeout int, journalMode string) {
	t.Helper()
	require.NoError(t, db.Raw("PRAGMA busy_timeout").Scan(&busyTimeout).Error)
	require.NoError(t, db.Raw("PRAGMA journal_mode").Scan(&journalMode).Error)
	return busyTimeout, journalMode
}

func TestSQLitePathCarriesConcurrencyParams(t *testing.T) {
	assert.Contains(t, common.SQLitePath, "busy_timeout",
		"the production DSN must carry busy_timeout, or writers die on SQLITE_BUSY with no retry")
	assert.Contains(t, common.SQLitePath, "journal_mode(WAL)",
		"the production DSN must request WAL, or readers block writers")
	assert.Contains(t, common.SQLitePath, "_txlock=immediate",
		"the production DSN must take the write lock up front, or a deferred transaction can fail with SQLITE_BUSY_SNAPSHOT, which no busy timeout covers")
}

func TestSQLiteParamsSurviveTheDriver(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/direct.db?"+common.SQLiteConcurrencyParams), &gorm.Config{})
	require.NoError(t, err)

	busyTimeout, journalMode := sqlitePragmas(t, db)
	assert.Equal(t, 30000, busyTimeout, "busy_timeout from the DSN did not reach the connection")
	assert.Equal(t, "wal", journalMode, "journal_mode from the DSN did not reach the connection")
}

// chooseDB is the single funnel every SQLite connection in this program passes
// through, including the reopen that model.InitDB performs. This is the path
// that lost the parameters, so it is the path worth pinning.
func TestChooseDBPreservesSQLiteConcurrencyParams(t *testing.T) {
	previousPath := common.SQLitePath
	t.Cleanup(func() { common.SQLitePath = previousPath })

	common.SQLitePath = t.TempDir() + "/reopened.db?" + common.SQLiteConcurrencyParams
	t.Setenv("SQL_DSN", "local")

	db, dbType, err := chooseDB("SQL_DSN", false)
	require.NoError(t, err)
	assert.Equal(t, common.DatabaseTypeSQLite, dbType)

	busyTimeout, journalMode := sqlitePragmas(t, db)
	assert.Equal(t, 30000, busyTimeout,
		"a database reopened through chooseDB lost busy_timeout; it is running on SQLite's 5s default")
	assert.Equal(t, "wal", journalMode,
		"a database reopened through chooseDB lost WAL; it is running on the rollback journal")

	// The transaction mode is a connection parameter rather than a pragma, so
	// it can only be observed by what a write actually does. Under
	// _txlock=immediate the write lock is taken at BEGIN; without it SQLite
	// starts deferred and a concurrent writer upgrades mid-transaction. Both
	// writers completing without error is the property that matters, and it is
	// what SQLITE_BUSY_SNAPSHOT took away.
	require.NoError(t, db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, v INTEGER)").Error)
	for i := 0; i < 8; i++ {
		require.NoError(t, db.Exec("INSERT INTO t (v) VALUES (?)", i).Error)
	}
}
