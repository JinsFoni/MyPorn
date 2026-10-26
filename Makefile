GO ?= go
PNPM ?= pnpm
export GOMODCACHE := $(CURDIR)/.gocache/mod
export GOCACHE := $(CURDIR)/.gocache/build
export GOPATH := $(CURDIR)/.gocache/gopath
export GOTMPDIR := $(CURDIR)/.gocache/tmp

.PHONY: all deps web build run backend test fmt vet clean dev-web

all: build

deps:
	mkdir -p "$(GOMODCACHE)" "$(GOCACHE)" "$(GOPATH)" "$(GOTMPDIR)"
	$(GO) mod download

# 构建前端并把产物同步到 Go 的 embed 目录
web:
	$(PNPM) --dir web install
	$(PNPM) --dir web build
	rm -rf internal/webui/dist
	cp -R web/dist internal/webui/dist

backend:
	$(GO) build -trimpath -ldflags "-s -w" -o bin/myporn ./cmd/myporn

build: web backend

run:
	./bin/myporn -root .

dev-web:
	$(PNPM) --dir web dev

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

clean:
	rm -rf bin internal/webui/dist web/dist
