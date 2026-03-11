BINARY   := docksmith
PKG      := .
GOFLAGS  := CGO_ENABLED=0
LDFLAGS  := -ldflags='-extldflags "-static"'

.PHONY: all build clean setup test

all: build

build:
	$(GOFLAGS) go build $(LDFLAGS) -o $(BINARY) $(PKG)
	@echo "Built static binary: ./$(BINARY)"

setup: build
	python3 scripts/setup_base.py

clean:
	rm -f $(BINARY)

# Run the full demo sequence against the sample app.
demo: build
	@echo "=== Cold build ==="
	./$(BINARY) build -t myapp:latest sample
	@echo ""
	@echo "=== Warm build (should be all CACHE HIT) ==="
	./$(BINARY) build -t myapp:latest sample
	@echo ""
	@echo "=== List images ==="
	./$(BINARY) images
	@echo ""
	@echo "=== Run container ==="
	./$(BINARY) run myapp:latest
	@echo ""
	@echo "=== Run with env override ==="
	./$(BINARY) run -e GREETING=Howdy myapp:latest

# Quick lint
vet:
	go vet ./...