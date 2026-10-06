# syntax=docker/dockerfile:1.7

# ---------------------------------------------------------------------------
# 多阶段构建：静态编译 → distroless 运行镜像。
#
# 三条硬性约束，每一条都对应一个具体的攻击面：
#
#  1. CGO_ENABLED=0 —— 产出静态二进制，运行镜像里可以没有 libc，
#     从而能落在 distroless/static 上。一个没有 shell、没有包管理器、
#     没有 libc 的镜像，即使应用被 RCE，攻击者也没有可用的「下水管道」。
#  2. 只拷贝二进制与前端产物 —— 源码、go 工具链、模块缓存都不进运行镜像。
#     它们不带来任何运行时能力，只会贡献 CVE 与镜像体积。
#  3. 非 root 用户（distroless 的 :nonroot，uid 65532）——
#     容器逃逸利用的第一步通常是「我是 root，我可以…」。
# ---------------------------------------------------------------------------

# ---------- 构建阶段 ----------
FROM golang:1.25 AS builder

WORKDIR /src

# 先只拷贝依赖清单：只要 go.mod/go.sum 没变，依赖层就能命中缓存，
# 改业务代码不会重新下载整个模块图。
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

# -trimpath 去掉二进制里的本地绝对路径（构建机目录结构属于不必要的信息泄漏）
# -s -w     去掉符号表与 DWARF（体积 + 逆向难度）
# -X        注入版本：二进制自报版本，后台与 /metrics 用同一份取值
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath \
      -ldflags="-s -w \
        -X github.com/atop0914/agentbot/internal/version.Version=${VERSION} \
        -X github.com/atop0914/agentbot/internal/version.GitCommit=${COMMIT} \
        -X github.com/atop0914/agentbot/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/agentbot ./cmd/agentbot

# 以非 root 身份构造运行目录树。运行阶段直接 COPY --from，
# 目录属主一路带过去，不需要在最终镜像里 chown（distroless 没有 chown）。
RUN mkdir -p /out/data/fs /out/data/storage /out/web && \
    printf '%s\n' \
      '<!doctype html><meta charset="utf-8"><title>AgentBot</title>' \
      '<h1>AgentBot</h1><p>Admin console assets are not bundled in this image.</p>' \
      > /out/web/index.html

# ---------- 运行阶段 ----------
FROM gcr.io/distroless/static-debian12:nonroot

# 版本信息以 OCI label 形式带出，便于镜像仓库侧溯源。
ARG VERSION=dev
ARG COMMIT=unknown
LABEL org.opencontainers.image.title="agentbot" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.licenses="MIT"

WORKDIR /app

COPY --from=builder --chown=65532:65532 /out/agentbot /app/agentbot
COPY --from=builder --chown=65532:65532 /out/web /srv/agentbot/web
COPY --from=builder --chown=65532:65532 /out/data /var/lib/agentbot

# 默认配置对齐 docs/deployment.md：数据目录可写（沙箱与存储），
# 其余文件系统只读（由 compose 的 read_only + tmpfs 强制）。
ENV AGENTBOT_SERVER_HOST=0.0.0.0 \
    AGENTBOT_SERVER_PORT=8080 \
    AGENTBOT_FILESYSTEM_ROOT=/var/lib/agentbot/fs \
    AGENTBOT_STORAGE_LOCAL_PATH=/var/lib/agentbot/storage \
    AGENTBOT_ADMIN_CONSOLE_DIR=/srv/agentbot/web

# 8080 业务 / 9090 指标
EXPOSE 8080 9090

# distroless 没有 shell，因此 exec 形式是唯一可行的写法，
# 也顺带保证 PID 1 是 agentbot 本身：它会正确接收 SIGTERM 并优雅停机。
# 用 shell 形式（CMD "agentbot"）在没有 shell 的镜像里会直接失败。
USER 65532:65532
ENTRYPOINT ["/app/agentbot"]
