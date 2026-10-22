package common

import (
	"net/url"
	"strings"
)

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
// 这个绝对路径时，busy_timeout / WAL / BEGIN IMMEDIATE 被一起丢掉。
//
// 参数丢掉后到底发生了什么，实测结论纠正了旧注释里「busy_timeout 变回 0」的
// 错误说法：纯 Go 驱动在每个连接上会无条件先执行 `PRAGMA BUSY_TIMEOUT(5000)`
// （驱动默认 5000ms，不是 0），journal_mode 则回落到 SQLite 默认的 `delete`
// （不是 WAL）。真正的根因是 **WAL 丢失**：没有 WAL 时 writer 会 block reader、
// reader 会 block writer，加上 _txlock=immediate 缺失，并发写走 busy_timeout 排队
// 也压不住，最终表现成 SQLITE_BUSY。生产日志实测共 20 条 "database is locked"
// （10/19 2 条、10/20 2 条、10/21 16 条），分布在计费结算 1 条、日志落库 8 条、
// 用户配额 7 条、渠道配额 2 条、系统实例 2 条。
//
// 运维改的只是「哪个文件」，不是「要不要并发参数」；后者是代码级的并发正确性
// 前提，不该因为指了个绝对路径就消失。所以这里把「路径」与「参数」分开：
// 环境变量只提供路径，并发参数始终由代码补上。
//
// 三个参数**各自独立**判断，绝不互相牵连：
//   - busy_timeout：已存在则保留调用方给的值，缺失才补 30000（绝不覆盖）；
//   - journal_mode：已存在则保留调用方给的值，缺失才补 WAL；
//   - _txlock：缺失时始终补 immediate（这是并发正确性的硬前提）。
//
// 旧实现用一个 `||` 把三个参数绑在一起，有两个 bug：(a) 只传 busy_timeout 时，
// 代码把整串三个参数又追加一遍，第二个 busy_timeout 按序覆盖了调用方显式给的
// 值；(b) 传了 busy_timeout + journal_mode 却没传 _txlock 时，直接原样返回，
// _txlock=immediate 被静默丢掉。现在三者独立判断，各自补缺，永不覆盖已存在的值。
func BuildSQLiteDSN(path string) string {
	if path == "" {
		return SQLitePath
	}
	// 拆出路径与查询串；查询串用 url.ParseQuery 解析，按「键」判断而非子串匹配，
	// 避免把值里的字符误判成已存在，也避免重复追加。
	base := path
	rawQuery := ""
	hadQuery := false
	if idx := strings.Index(path, "?"); idx >= 0 {
		base = path[:idx]
		rawQuery = path[idx+1:]
		hadQuery = true
	}

	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		// 查询串解析失败说明调用方给的不是合法 URI 查询，原样返回让驱动自己报错，
		// 而不是在这里把一个坏 DSN 再加工一遍。
		return path
	}

	var missing []string
	if !hasPragma(values, "busy_timeout") {
		missing = append(missing, "_pragma=busy_timeout(30000)")
	}
	if !hasPragma(values, "journal_mode") {
		missing = append(missing, "_pragma=journal_mode(WAL)")
	}
	if values.Get("_txlock") == "" {
		missing = append(missing, "_txlock=immediate")
	}

	if len(missing) == 0 {
		return path
	}
	// 原样保留调用方给的查询串，只在末尾追加缺失的参数。
	if hadQuery {
		return base + "?" + rawQuery + "&" + strings.Join(missing, "&")
	}
	return base + "?" + strings.Join(missing, "&")
}

// hasPragma 判断 query 的 _pragma 参数里是否已包含某个 pragma 名（忽略大小写），
// 只比到左括号之前，即 `busy_timeout(30000)` 与 `busy_timeout(1000)` 都算命中
// `busy_timeout`。这样可以识别调用方已经显式给了该 pragma，从而不覆盖。
func hasPragma(values url.Values, name string) bool {
	for _, p := range values["_pragma"] {
		if i := strings.IndexAny(p, "("); i > 0 {
			p = p[:i]
		}
		if strings.EqualFold(strings.TrimSpace(p), name) {
			return true
		}
	}
	return false
}
