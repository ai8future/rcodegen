VERSION := $(shell cat VERSION)
LDFLAGS := -ldflags="-w -s"
BINDIR  := bin

BINS := rclaude rcodex rgemini ropencode rkilo rcodegen rserve rbatch

.DEFAULT_GOAL := build-all

.PHONY: build build-linux build-darwin build-all test clean lint deps run proto \
        e2e-localai-preflight e2e-ollama e2e-lmstudio e2e-localai-smoke e2e-localai-full \
        $(foreach b,$(BINS),$(b) $(b)-linux $(b)-darwin)

.NOTPARALLEL: e2e-localai-preflight e2e-ollama e2e-lmstudio e2e-localai-smoke e2e-localai-full

# --- Native build (one target per binary) ---
build: $(BINS)

$(foreach b,$(BINS),$(eval \
$(b): ; @rm -f $(BINDIR)/$(b) && CGO_ENABLED=0 go build $(LDFLAGS) -o $(BINDIR)/$(b) ./cmd/$(b)))

# --- Linux amd64 cross-compilation ---
build-linux: $(addsuffix -linux,$(BINS))

$(foreach b,$(BINS),$(eval \
$(b)-linux: ; @rm -f $(BINDIR)/$(b)-linux-amd64 && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BINDIR)/$(b)-linux-amd64 ./cmd/$(b)))

# --- Darwin arm64 cross-compilation ---
build-darwin: $(addsuffix -darwin,$(BINS))

$(foreach b,$(BINS),$(eval \
$(b)-darwin: ; @rm -f $(BINDIR)/$(b)-darwin-arm64 && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BINDIR)/$(b)-darwin-arm64 ./cmd/$(b)))

# --- Build all platforms + launcher scripts ---
build-all: build-linux build-darwin
	@for bin in $(BINS); do \
		cp scripts/launcher-$$bin.sh $(BINDIR)/$$bin && chmod +x $(BINDIR)/$$bin; \
	done

# --- Test ---
test:
	go test -v -race -cover ./...

# --- Clean ---
clean:
	@rm -rf $(BINDIR)/

# --- Lint ---
lint:
	golangci-lint run ./...

# --- Deps ---
deps:
	go mod download
	go mod tidy

# --- Run (default binary) ---
run: rcodegen
	./$(BINDIR)/rcodegen

# --- Protobuf ---
proto:
	PATH="$$PATH:$$(go env GOPATH)/bin" protoc --go_out=. --go-grpc_out=. proto/rserve.proto
	@# Move generated files to pkg/server/pb/ if protoc placed them under module path
	@if [ -d rcodegen/pkg/server/pb ]; then \
		mv rcodegen/pkg/server/pb/*.go pkg/server/pb/ && rm -rf rcodegen; \
	fi

# --- Opt-in live local-runtime E2E tests (serialized and lifecycle guarded) ---
e2e-localai-preflight: rserve rbatch
	./scripts/e2e-localai.sh preflight

e2e-ollama: rserve rbatch
	./scripts/e2e-localai.sh ollama

e2e-lmstudio: rserve rbatch
	./scripts/e2e-localai.sh lmstudio

e2e-localai-smoke: rserve rbatch
	./scripts/e2e-localai.sh smoke

e2e-localai-full: rserve rbatch
	./scripts/e2e-localai.sh full
