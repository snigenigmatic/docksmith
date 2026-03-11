binary := "docksmith"
pkg := "."
goflags := "CGO_ENABLED=0"
ldflags := "-ldflags='-extldflags \"-static\"'"

list:

# Default recipe
default: build

# Build the binary
build:
    {{goflags}} go build {{ldflags}} -o {{binary}} {{pkg}}
    @echo "Built static binary: ./{{binary}}"

# Setup the base image
setup: build
    python3 scripts/setup_base.py

# Clean up built binary
clean:
    rm -f {{binary}}

# Run the full demo sequence against the sample app
demo: build
    @echo "=== Cold build ==="
    ./{{binary}} build -t myapp:latest sample
    @echo ""
    @echo "=== Warm build (should be all CACHE HIT) ==="
    ./{{binary}} build -t myapp:latest sample
    @echo ""
    @echo "=== List images ==="
    ./{{binary}} images
    @echo ""
    @echo "=== Run container ==="
    ./{{binary}} run myapp:latest
    @echo ""
    @echo "=== Run with env override ==="
    ./{{binary}} run -e GREETING=Howdy myapp:latest


# Quick lint
vet:
    go vet ./...
