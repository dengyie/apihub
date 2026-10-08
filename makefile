WEB_DIR = ./web
API_DIR = .
DEV_WEB_PORT ?= 5173
DEV_COMPOSE_FILE = docker-compose.dev.yml
DEV_POSTGRES_SERVICE = postgres
DEV_API_SERVICE = new-api
DEV_POSTGRES_DB = new-api
DEV_POSTGRES_USER = root
DEV_SQLITE_PATH ?= one-api.db

# 模块全限定名。ldflags 的 -X 目标是 <模块路径>/common.Version，而这个字符串
# 之前是手抄的：模块从 github.com/QuantumNous/new-api 改名为
# github.com/dengyie/apihub 之后它没有跟着改，而 Go 链接器对 -X 指向一个不存在
# 的符号既不报错也不警告 —— `make build-api` 于是照常成功，产出 common.Version
# 停留在 "v0.0.0" 的二进制（见 README 开发章节）。从 go.mod 推导，这个字符串就
# 再也不会过期。
MODULE := $(shell go list -m)
VERSION_STAMP := $(MODULE)/common.Version

.PHONY: all build-web build-all-web verify-embed build-api start-api dev dev-api dev-api-rebuild dev-web reset-setup test

all: build-all-web start-api

build-web:
	@echo "Building web frontend..."
	@cd $(WEB_DIR) && bun install --frozen-lockfile
	@cd $(WEB_DIR) && DISABLE_ESLINT_PLUGIN='true' VITE_REACT_APP_VERSION=$$(cat ../VERSION) bun run build

build-all-web: build-web

# 前端嵌入完整性闸门。
#
# web/dist 被 .gitignore 排除、git 里 0 个跟踪文件，而 main.go 用 //go:embed
# 把它打进二进制。go:embed 对「目录不存在」「目录为空」都会报错（安全），但只要
# web/dist/index.html 存在 —— 哪怕是 0 字节占位文件 —— 构建就会成功，产出的是
# 一个「能启动、能响应 200、首页正文 0 字节」的二进制：后台白屏，且状态码查不
# 出来。2026-10-03 生产上因此白屏 2h41m。
#
# 这两个阈值必须与 main.go 的 minEmbeddedIndexBytes / minEmbeddedStaticFiles
# 以及 .github/workflows/build-release.yml 的同名步骤保持一致，改一处要改三处。
verify-embed:
	@set -e; \
	if [ ! -f $(WEB_DIR)/dist/index.html ]; then \
		echo "ERROR: $(WEB_DIR)/dist/index.html 不存在 —— 先跑 'make build-web'"; exit 1; \
	fi; \
	size=$$(wc -c < $(WEB_DIR)/dist/index.html | tr -d ' '); \
	if [ "$$size" -lt 200 ]; then \
		echo "ERROR: index.html 仅 $$size 字节，疑似占位文件；真实构建约 1KB"; exit 1; \
	fi; \
	if [ ! -d $(WEB_DIR)/dist/static ]; then \
		echo "ERROR: $(WEB_DIR)/dist/static 缺失：前端资源目录不存在"; exit 1; \
	fi; \
	assets=$$(find $(WEB_DIR)/dist/static -type f | wc -l | tr -d ' '); \
	if [ "$$assets" -lt 10 ]; then \
		echo "ERROR: static 资源只有 $$assets 个，远少于真实构建应有的数量"; exit 1; \
	fi; \
	echo "embed 校验通过：index.html $$size 字节，static 资源 $$assets 个"

# 本地构建生产二进制。版本串绑定 commit（v29.16 起的约定），与 CI 保持同一形状：
# 三个不同产物共用同一个标签，事后无法判断跑的是哪次提交。
#
# 构建后立刻断言版本确实被戳进去了。-X 对不存在的符号是静默的，光靠构建成功
# 说明不了任何事 —— 而这个闸门要挡住的就是「构建成功、版本号是 v0.0.0」。
build-api: verify-embed
	@echo "Building api binary..."
	@cd $(API_DIR) && go build \
		-ldflags "-s -w -X '$(VERSION_STAMP)=$$(cat VERSION)+$$(git rev-parse --short=9 HEAD)'" \
		-o new-api
	@want="$$(cat VERSION)+$$(git rev-parse --short=9 HEAD)"; \
	got=$$(cd $(API_DIR) && ./new-api --version 2>/dev/null | tr -d '[:space:]'); \
	if [ "$$got" != "$$want" ]; then \
		echo "ERROR: 版本戳没有生效：期望 '$$want'，实际 '$$got'。"; \
		echo "       检查 -X 的目标符号 '$(VERSION_STAMP)' 是否存在。"; \
		exit 1; \
	fi; \
	echo "version stamped: $$got"

start-api: verify-embed
	@echo "Starting api dev server..."
	@cd $(API_DIR) && go run main.go &

dev-api:
	@echo "Starting api services (docker)..."
	@docker compose -f $(DEV_COMPOSE_FILE) up -d

dev-api-rebuild:
	@echo "Rebuilding and starting api service (docker)..."
	@docker compose -f $(DEV_COMPOSE_FILE) up -d --build $(DEV_API_SERVICE)

dev-web:
	@echo "Starting web frontend dev server..."
	@echo "Web frontend: http://localhost:$(DEV_WEB_PORT)"
	@cd $(WEB_DIR) && bun install
	@cd $(WEB_DIR) && bun run dev -- --host 0.0.0.0 --port $(DEV_WEB_PORT)

dev: dev-api dev-web

# The main package embeds the ignored web/dist output and is covered after build-web.
test:
	@echo "Testing root Go module..."
	@root_module=$$(GOWORK=off go list -m); \
		root_packages=$$(GOWORK=off go list -e ./... | grep -vxF "$$root_module"); \
		GOWORK=off go test $$root_packages
	@echo "Testing relaykit Go module..."
	@cd relaykit && GOWORK=off go test ./...

reset-setup:
	@echo "Resetting local setup wizard state..."
	@if docker compose -f $(DEV_COMPOSE_FILE) ps --services --status running | grep -qx "$(DEV_POSTGRES_SERVICE)"; then \
		echo "Detected running docker dev PostgreSQL. Removing setup record and root users..."; \
		docker compose -f $(DEV_COMPOSE_FILE) exec -T $(DEV_POSTGRES_SERVICE) \
			psql -U $(DEV_POSTGRES_USER) -d $(DEV_POSTGRES_DB) \
			-c 'DELETE FROM setups;' \
			-c 'DELETE FROM users WHERE role = 100;' \
			-c "DELETE FROM options WHERE key IN ('SelfUseModeEnabled', 'DemoSiteEnabled');"; \
		echo "Restarting docker dev api so setup status is recalculated..."; \
		docker compose -f $(DEV_COMPOSE_FILE) restart $(DEV_API_SERVICE); \
	elif db_path="$${SQLITE_PATH:-$(DEV_SQLITE_PATH)}"; db_path="$${db_path%%\?*}"; [ -f "$$db_path" ]; then \
		db_path="$${SQLITE_PATH:-$(DEV_SQLITE_PATH)}"; \
		db_path="$${db_path%%\?*}"; \
		echo "Detected local SQLite database: $$db_path"; \
		sqlite3 "$$db_path" \
			"DELETE FROM setups; DELETE FROM users WHERE role = 100; DELETE FROM options WHERE key IN ('SelfUseModeEnabled', 'DemoSiteEnabled');"; \
		echo "SQLite setup state reset. Restart the local api process before testing the setup wizard."; \
	else \
		echo "No running docker dev PostgreSQL or local SQLite database found."; \
		echo "Start the dev stack with 'make dev-api', or set SQLITE_PATH/DEV_SQLITE_PATH to your local SQLite database."; \
		exit 1; \
	fi
