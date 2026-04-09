set shell := ["bash", "-cu"]

binary := "./docksmith"

# Show available recipes
@default:
  just --list

# Build the CLI binary
build:
  go build -o docksmith .

# One-time base image import (requires network)
setup-base:
  go run setup_base.go

# Re-import base image if shared layers were removed via rmi
reset-base: setup-base

# Run tests
@test:
  go test ./...

# Format Go files in repo root
fmt:
  gofmt -w *.go

# Build sample image (cold or warm depending on cache state)
cold-build:
  {{binary}} build -t myapp:latest .

warm-build:
  {{binary}} build -t myapp:latest .

# Build sample image without cache
no-cache:
  {{binary}} build -t myapp:latest . --no-cache

# List images
images:
  {{binary}} images

# Run image using CMD
run:
  {{binary}} run myapp:latest

# Run with env override
run-env key="GREETING" value="Hola":
  {{binary}} run -e {{key}}={{value}} myapp:latest

# Run with command override
run-cmd +args:
  {{binary}} run myapp:latest {{args}}

# Remove sample image and its layers
rmi:
  {{binary}} rmi myapp:latest

# Quick demo flow
demo: build cold-build warm-build images run
  @echo "Demo core steps completed."
  @echo "Try: just run-env GREETING Hi"
