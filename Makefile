VERSION := $(shell cat VERSION | tr -d '[:space:]')
LDFLAGS := -X rcodegen/pkg/runner.Version=$(VERSION)
BINDIR  := bin

.PHONY: all rclaude rcodex rgemini rcodegen rserve clean test proto

all: rclaude rcodex rgemini rcodegen rserve

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

proto:
	PATH="$$PATH:$$(go env GOPATH)/bin" protoc --go_out=. --go-grpc_out=. proto/rserve.proto
	@# Move generated files to pkg/server/pb/ if protoc placed them under module path
	@if [ -d rcodegen/pkg/server/pb ]; then \
		mv rcodegen/pkg/server/pb/*.go pkg/server/pb/ && rm -rf rcodegen; \
	fi

clean:
	rm -f $(BINDIR)/rclaude $(BINDIR)/rcodex $(BINDIR)/rgemini $(BINDIR)/rcodegen $(BINDIR)/rserve

test:
	go test ./pkg/...
