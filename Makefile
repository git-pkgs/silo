.PHONY: all test lint cover bench fuzz smoke check build

GO        ?= go
LINTERS   := gocritic,gocognit,gocyclo,maintidx,dupl,mnd,unparam,ireturn,goconst,errcheck
COVER_MIN := 80

all: check

build:
	$(GO) build -o silo ./cmd/silo

test:
	$(GO) test -race -shuffle=on ./...

cover:
	$(GO) test -coverprofile=cover.out ./internal/...
	$(GO) tool cover -func=cover.out | tail -1
	@pct=$$($(GO) tool cover -func=cover.out | tail -1 | grep -o '[0-9.]*' | tail -1); \
	  awk -v p=$$pct -v m=$(COVER_MIN) 'BEGIN{exit (p<m)}' || \
	  (echo "coverage $$pct% < $(COVER_MIN)%" && exit 1)

lint:
	golangci-lint run ./...
	govulncheck ./...
	deadcode -test ./...
	gosec -quiet ./...

bench:
	$(GO) test -bench . -benchmem -run '^$$' ./internal/... | tee bench.txt

fuzz:
	$(GO) test -fuzz=FuzzCommandList -fuzztime=60s ./internal/receive

smoke:
	$(GO) test -run TestScript -count=3 ./...

check: test cover lint
