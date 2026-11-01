# syntax=docker/dockerfile:1

# MyPorn 单镜像：前端产物 go:embed 进二进制，运行层只留「二进制 + ffmpeg + yt-dlp + deno」。
#
# 为什么是这三样外部程序：
#   ffmpeg —— 分轨合流（B 站 DASH / YouTube HLS）、分段下载后封装 mp4、AVIF 封面兜底解码；
#   yt-dlp —— YouTube 解析与下载，站点一变就得跟着升，所以走 pip 装最新版而不是发行版仓库；
#   deno   —— yt-dlp 的 JS 运行时，解 YouTube 的 n 挑战（实测那一步 0.52 s，缺了它拿不到高清档）。
#
# SQLite 用 modernc.org/sqlite（纯 Go），所以 CGO_ENABLED=0 就能编，amd64/arm64 一份 Dockerfile 通吃。
#
# 国内网络构建：默认源在国外，本机实测 proxy.golang.org 在这个 Docker VM 里 3/3 断流、
# goproxy.cn 3/3 正常，所以留了三个加速口（不填就保持官方源）：
#   docker build --build-arg GOPROXY=https://goproxy.cn,direct \
#                --build-arg APK_MIRROR=https://mirrors.aliyun.com/alpine \
#                --build-arg PIP_INDEX=https://pypi.tuna.tsinghua.edu.cn/simple .

# ---- 1. 前端：Svelte 5 + Vite → dist ----
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src
# pnpm 版本跟着 lockfile（lockfileVersion 9.0）走，固定版本免得 CI 与本地装出两套依赖树
RUN corepack enable && corepack prepare pnpm@11.27.1 --activate
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# ---- 2. 后端：把 dist 嵌进去，按目标架构交叉编译 ----
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH
# VERSION 会写进 /healthz 的 version 字段，release 构建时由 workflow 注入 tag
ARG VERSION=dev
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
# 覆盖仓库里那个占位 dist（.dockerignore 已经把源码树里的 dist 排除了）
COPY --from=web /src/dist ./internal/webui/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.buildVersion=$VERSION" -o /out/myporn ./cmd/myporn

# ---- 3. 运行层 ----
FROM alpine:3.22 AS runtime

ARG APK_MIRROR=
ARG PIP_INDEX=

# pip 这是「构建期」取最新版 yt-dlp：YouTube 一变就得重建镜像，这是有意为之。
# --resume-retries：wheel 才 3 MB 级别，网络抖一下就 incomplete-download（本机实测过），要能续传。
RUN if [ -n "$APK_MIRROR" ]; then \
        sed -i "s#https://dl-cdn.alpinelinux.org/alpine#$APK_MIRROR#g" /etc/apk/repositories; \
    fi \
 && apk add --no-cache \
        ca-certificates tzdata tini \
        ffmpeg \
        python3 py3-pip deno \
 && python3 -m pip install --no-cache-dir --break-system-packages --upgrade \
        --retries 5 --resume-retries 5 ${PIP_INDEX:+--index-url "$PIP_INDEX"} yt-dlp \
 && rm -rf /root/.cache

# 非 root 跑（uid/gid 1000）：媒体库走 bind mount 时，宿主机上写出来的文件属主才不是 root。
# 宿主目录要先 chown 1000:1000，见 docker-compose.yml 里的注释。
RUN addgroup -S -g 1000 myporn \
 && adduser -S -u 1000 -G myporn -h /app myporn \
 && mkdir -p /app /data /media \
 && chown -R myporn:myporn /app /data /media

COPY --from=build /out/myporn /usr/local/bin/myporn

# -root /data：.env.local 从数据卷读、工作目录也在卷里，容器重建不丢任何状态。
# 下面这些默认值只是「不配也能起」；实际部署按 docker-compose.yml 走环境变量。
ENV HOME=/app \
    DATA_DIR=/data \
    DATABASE_PATH=/data/myporn.db \
    LIBRARY_ROOT=/media \
    PORT=8080 \
    FFMPEG_PATH=ffmpeg \
    FFPROBE_PATH=ffprobe \
    YTDLP_PATH=yt-dlp

EXPOSE 8080
VOLUME ["/data", "/media"]

# 走 busybox 的 wget，不额外装 curl
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD wget -qO- "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1 || exit 1

USER myporn
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/myporn", "-root", "/data"]
