VERSION := $(shell cat VERSION | tr -d '[:space:]')
LDFLAGS := -X rcodegen/pkg/runner.Version=$(VERSION)
BINDIR  := bin

.PHONY: all rclaude rcodex rgemini rcodegen rserve rbatch linux rclaude_linux rcodex_linux rgemini_linux rcodegen_linux rserve_linux rbatch_linux clean test proto

all: rclaude rcodex rgemini rcodegen rserve rbatch

rclaude:
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rclaude ./cmd/rclaude

rcodex:
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rcodex ./cmd/rcodex

rgemini:
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rgemini ./cmd/rgemini

rcodegen:
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rcodegen ./cmd/rcodegen

rserve:
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rserve ./cmd/rserve

rbatch:
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rbatch ./cmd/rbatch

# Linux amd64 cross-compiled binaries
linux: rclaude_linux rcodex_linux rgemini_linux rcodegen_linux rserve_linux rbatch_linux

rclaude_linux:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rclaude_linux ./cmd/rclaude

rcodex_linux:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rcodex_linux ./cmd/rcodex

rgemini_linux:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rgemini_linux ./cmd/rgemini

rcodegen_linux:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rcodegen_linux ./cmd/rcodegen

rserve_linux:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rserve_linux ./cmd/rserve

rbatch_linux:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rbatch_linux ./cmd/rbatch

proto:
	PATH="$$PATH:$$(go env GOPATH)/bin" protoc --go_out=. --go-grpc_out=. proto/rserve.proto
	@# Move generated files to pkg/server/pb/ if protoc placed them under module path
	@if [ -d rcodegen/pkg/server/pb ]; then \
		mv rcodegen/pkg/server/pb/*.go pkg/server/pb/ && rm -rf rcodegen; \
	fi

clean:
	rm -f $(BINDIR)/rclaude $(BINDIR)/rcodex $(BINDIR)/rgemini $(BINDIR)/rcodegen $(BINDIR)/rserve $(BINDIR)/rbatch
	rm -f $(BINDIR)/rclaude_linux $(BINDIR)/rcodex_linux $(BINDIR)/rgemini_linux $(BINDIR)/rcodegen_linux $(BINDIR)/rserve_linux $(BINDIR)/rbatch_linux

test:
	go test ./pkg/...
