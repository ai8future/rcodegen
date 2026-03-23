VERSION := $(shell cat VERSION | tr -d '[:space:]')
LDFLAGS := -X rcodegen/pkg/runner.Version=$(VERSION)
BINDIR  := bin

BINS := rclaude rcodex rgemini rcodegen rserve rbatch

.PHONY: all $(BINS) linux darwin build-all clean test proto \
        rclaude_linux rcodex_linux rgemini_linux rcodegen_linux rserve_linux rbatch_linux \
        rclaude_darwin rcodex_darwin rgemini_darwin rcodegen_darwin rserve_darwin rbatch_darwin

all: $(BINS)

rclaude:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rclaude ./cmd/rclaude

rcodex:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rcodex ./cmd/rcodex

rgemini:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rgemini ./cmd/rgemini

rcodegen:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rcodegen ./cmd/rcodegen

rserve:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rserve ./cmd/rserve

rbatch:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rbatch ./cmd/rbatch

# Linux amd64
linux: rclaude_linux rcodex_linux rgemini_linux rcodegen_linux rserve_linux rbatch_linux

rclaude_linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rclaude-linux-amd64 ./cmd/rclaude

rcodex_linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rcodex-linux-amd64 ./cmd/rcodex

rgemini_linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rgemini-linux-amd64 ./cmd/rgemini

rcodegen_linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rcodegen-linux-amd64 ./cmd/rcodegen

rserve_linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rserve-linux-amd64 ./cmd/rserve

rbatch_linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rbatch-linux-amd64 ./cmd/rbatch

# Darwin arm64
darwin: rclaude_darwin rcodex_darwin rgemini_darwin rcodegen_darwin rserve_darwin rbatch_darwin

rclaude_darwin:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rclaude-darwin-arm64 ./cmd/rclaude

rcodex_darwin:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rcodex-darwin-arm64 ./cmd/rcodex

rgemini_darwin:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rgemini-darwin-arm64 ./cmd/rgemini

rcodegen_darwin:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rcodegen-darwin-arm64 ./cmd/rcodegen

rserve_darwin:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rserve-darwin-arm64 ./cmd/rserve

rbatch_darwin:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/rbatch-darwin-arm64 ./cmd/rbatch

# Build all platforms + launchers
build-all: linux darwin
	@for bin in $(BINS); do cp scripts/launcher-$$bin.sh $(BINDIR)/$$bin && chmod +x $(BINDIR)/$$bin; done

clean:
	rm -f $(foreach bin,$(BINS),$(BINDIR)/$(bin) $(BINDIR)/$(bin)-linux-amd64 $(BINDIR)/$(bin)-darwin-arm64)

test:
	go test ./pkg/...

proto:
	PATH="$$PATH:$$(go env GOPATH)/bin" protoc --go_out=. --go-grpc_out=. proto/rserve.proto
	@# Move generated files to pkg/server/pb/ if protoc placed them under module path
	@if [ -d rcodegen/pkg/server/pb ]; then \
		mv rcodegen/pkg/server/pb/*.go pkg/server/pb/ && rm -rf rcodegen; \
	fi
