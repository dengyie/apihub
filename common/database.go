package common

import "strings"

type DatabaseType string

const (
	DatabaseTypeMySQL      DatabaseType = "mysql"
	DatabaseTypeSQLite     DatabaseType = "sqlite"
	DatabaseTypePostgreSQL DatabaseType = "postgres"
	DatabaseTypeClickHouse DatabaseType = "clickhouse"
)

var mainDatabaseType = DatabaseTypeSQLite
var logDatabaseType = DatabaseTypeSQLite

func MainDatabaseType() DatabaseType {
	return mainDatabaseType
}

func LogDatabaseType() DatabaseType {
	return logDatabaseType
}

func SetMainDatabaseType(databaseType DatabaseType) {
	mainDatabaseType = databaseType
}

func SetLogDatabaseType(databaseType DatabaseType) {
	logDatabaseType = databaseType
}

func SetDatabaseTypes(mainType DatabaseType, logType DatabaseType) {
	mainDatabaseType = mainType
	logDatabaseType = logType
}

func UsingMainDatabase(databaseType DatabaseType) bool {
	return mainDatabaseType == databaseType
}

func UsingLogDatabase(databaseType DatabaseType) bool {
	return logDatabaseType == databaseType
}

// SQLitePath is the DSN for the default SQLite database. It uses WAL journal
// mode so readers are never blocked by the single writer, plus a 30s busy
// timeout for writers to queue.
//
// Two details are non-obvious and both are required for concurrent correctness:
//
//  1. The busy timeout must be passed as a `_pragma=busy_timeout(30000)` DSN
//     parameter. The pure-Go driver (modernc.org/sqlite, used through
//     github.com/glebarez/sqlite) silently ignores the plain `_busy_timeout=`
//     form, so without this the effective timeout stays at SQLite's 5s default
//     and concurrent writes surface as "database is locked" (see #6805).
//
//  2. `_txlock=immediate` (BEGIN IMMEDIATE) must be enabled. Without it, a
//     transaction that first SELECTs (establishing a read snapshot) and then
//     writes can hit SQLITE_BUSY_SNAPSHOT when another connection commits in
//     between; the busy handler does not cover that case, so the write fails
//     instantly no matter the timeout. BEGIN IMMEDIATE takes the write lock up
//     front, so writers serialize through the busy timeout instead of dying on
//     a stale snapshot. Autocommit SELECTs stay concurrent because WAL keeps
//     readers unlocked.
var SQLitePath = "one-api.db?" + SQLiteConcurrencyParams

// SQLiteConcurrencyParams carries the query parameters described above and
// nothing else, so a test can build a DSN for a throwaway file that behaves
// like the production one. It is a separate constant because SQLitePath itself
// is reassigned at runtime -- tests point it at a temporary database -- and
// anything derived from it at the point of use silently loses the pragmas the
// second time it is read.
const SQLiteConcurrencyParams = "_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_txlock=immediate"

// BuildSQLiteDSN 给一个 SQLite 文件路径补齐并发参数。
//
// 存在的理由是一个真实的生产事故：SQLITE_PATH 环境变量原本是**整串替换**
// SQLitePath 的（`SQLitePath = os.Getenv("SQLITE_PATH")`），而 SQLitePath 自带
// 的正是上面那三个并发参数。于是运维把库指到 /data/new-api/data/new-api.db
// 这个绝对路径时，busy_timeout / WAL / BEGIN IMMEDIATE 被一起丢掉 ——
// PRAGMA busy_timeout 变回 0，写操作遇到并发立刻 SQLITE_BUSY，当天就丢了
// 计费结算、日志落库、渠道配额更新共 17 笔。
//
// 运维改的只是「哪个文件」，不是「要不要并发参数」；后者是代码级的并发正确性
// 前提，不该因为指了个绝对路径就消失。所以这里把「路径」与「参数」分开：
// 环境变量只提供路径，并发参数始终由代码补上。
//
// 幂等：DSN 里已经有同名参数时不再重复追加（重复的 _pragma 会被驱动按序执行，
// 重复设置 busy_timeout 虽然无害，但会让日志里的 DSN 变得难读）。
func BuildSQLiteDSN(path string) string {
	if path == "" {
		return SQLitePath
	}
	// 已经带了查询串的，说明调用方自己拼过参数：只在缺并发参数时补。
	if idx := strings.Index(path, "?"); idx >= 0 {
		query := path[idx+1:]
		if !strings.Contains(query, "busy_timeout") || !strings.Contains(query, "journal_mode") {
			return path + "&" + SQLiteConcurrencyParams
		}
		return path
	}
	return path + "?" + SQLiteConcurrencyParams
}
