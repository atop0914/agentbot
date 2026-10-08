# AgentBot Makefile
#
# 设计原则：所有构建都注入版本三元组（Version/GitCommit/BuildDate）。
# 一个不知道自己是谁的二进制，在线上排障时会变成一场考古 ——
# 运维只能靠「这是上周三那个人部署的」来猜版本。版本号必须由 git 推导，
# 人不能手填：手填的版本号迟早会和实际代码分叉。

MODULE  := github.com/atop0914/agentbot
BINARY  := agentbot
CMD     := ./cmd/agentbot

# 版本推导优先级：git tag（精确匹配） > 最近 tag + 距离 + 脏标记 > dev。
# 用 --dirty 标记工作区有未提交改动，避免「跑的是未提交代码却自称 v1.0.0」。
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# -s -w 去掉符号表与调试信息。
# 注意 -trimpath 不能放进这里：它是 go build 的标志，不是链接器标志，
# 混进来会变成 `link: flag provided but not defined: -trimpath` 直接构建失败。
# 它由各目标的 go build 命令行显式带上。
LDFLAGS := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.GitCommit=$(COMMIT) \
	-X $(MODULE)/internal/version.BuildDate=$(DATE)

# 交叉编译矩阵；windows/arm64 故意缺席（无使用者，白占一次完整编译）。
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: help
help: ## 列出所有可用目标
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## 构建本机二进制到 bin/
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY) $(CMD)
	@echo "built bin/$(BINARY) ($(VERSION) $(COMMIT))"

.PHONY: build-all
build-all: ## 交叉编译全部平台到 dist/
	@mkdir -p dist
	@set -e; for platform in $(PLATFORMS); do \
		goos=$${platform%/*}; goarch=$${platform#*/}; \
		ext=""; [ "$$goos" = "windows" ] && ext=".exe"; \
		name="$(BINARY)_$(VERSION)_$${goos}_$${goarch}$$ext"; \
		echo "building $$name"; \
		GOOS=$$goos GOARCH=$$goarch CGO_ENABLED=0 \
			go build -trimpath -ldflags="$(LDFLAGS)" -o "dist/$$name" $(CMD); \
	done
	@cd dist && sha256sum * > checksums.txt && echo "checksums written to dist/checksums.txt"

.PHONY: test
test: ## 运行测试（-short，跳过容器依赖用例）
	go test -short -race -count=1 ./...

.PHONY: test-full
test-full: ## 运行全部测试（需要 Docker，本地不可用时必然失败）
	go test -race -count=1 ./...

.PHONY: vet
vet: ## 静态检查
	go vet ./...

.PHONY: lint
lint: ## golangci-lint（未安装时给出安装提示而非莫名失败）
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not found. install:"; \
		echo "  curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b \$$(go env GOPATH)/bin v2.12.2"; \
		exit 1; }
	golangci-lint run --timeout=5m

.PHONY: fmt
fmt: ## 格式化
	gofmt -s -w .

.PHONY: check
check: fmt vet test ## 本地提交前的完整闸门

.PHONY: run
run: ## 本地运行（需先导出 AUTH_JWT_SECRET / DATABASE_PASSWORD）
	go run $(CMD)

.PHONY: clean
clean: ## 清理构建产物
	rm -rf bin dist

.PHONY: version
version: ## 打印将被注入的版本三元组
	@echo "version=$(VERSION)"
	@echo "commit=$(COMMIT)"
	@echo "date=$(DATE)"
