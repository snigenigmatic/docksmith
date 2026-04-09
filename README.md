# Docksmith

A simplified Docker-like build and runtime system written in Go.

Docksmith can:
- Build images from a `Docksmithfile`
- Reuse cached layers for fast rebuilds
- Run containers in an isolated root filesystem
- List and remove local images

---

## Requirements

- Linux (required for namespace/chroot isolation)
- Go 1.22+

> `build` and `run` are designed to work offline.
> The only network step is the one-time base image import.

---

## Quick start (copy/paste)

### 1) Build the CLI binary

```bash
go build -o docksmith .
```

### 2) Import base image (one-time setup)

```bash
go run setup_base.go
```

This imports `alpine:3.18` into `~/.docksmith`.

### 3) Cold build (expect cache MISS)

```bash
./docksmith build -t myapp:latest .
```

### 4) Warm build (expect cache HIT)

```bash
./docksmith build -t myapp:latest .
```

### 5) List local images

```bash
./docksmith images
```

### 6) Run container with image CMD

```bash
./docksmith run myapp:latest
```

### 7) Run with env override

```bash
./docksmith run -e GREETING=Hola myapp:latest
```

### 8) Run with command override

```bash
./docksmith run myapp:latest /bin/sh -c 'echo custom command'
```

### 9) Build without cache

```bash
./docksmith build -t myapp:latest . --no-cache
```

### 10) Remove image and its layers

```bash
./docksmith rmi myapp:latest
```

---

## Demo flow (recommended)

1. `./docksmith build -t myapp:latest .` (cold build)
2. `./docksmith build -t myapp:latest .` (warm build)
3. Edit a source file (for example `main.sh`) and rebuild
4. `./docksmith images`
5. `./docksmith run myapp:latest`
6. `./docksmith run -e GREETING=Hi myapp:latest`
7. `./docksmith run myapp:latest /bin/sh -c 'echo isolated > /app/check.txt'`
8. Verify `/app/check.txt` does **not** appear on host
9. `./docksmith rmi myapp:latest`

---

## Local state layout

Docksmith stores data in:

```text
~/.docksmith/
  images/   # image manifests (json)
  layers/   # content-addressed layer tar files
  cache/    # build cache index
```

---

## Useful note about `rmi`

`rmi` removes the image manifest and all layers listed in that manifest.
Because layers are content-addressed and not reference-counted, removing one image can remove shared layers used by another image.

If you remove the shared alpine base layer, re-import it with:

```bash
go run setup_base.go
```
