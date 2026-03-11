# Docksmith

A simplified Docker-like build and runtime system built from scratch in Go. No daemon, no external container tooling — just Linux OS primitives. Built for Cloud Computing (UE23CS351B) PESU.

Docksmith teaches three things deeply:
- How **build caching and content-addressing** work
- How **process isolation** works at the OS level
- How **images are assembled from layers** and run as containers

---

## Requirements

- **Linux** (or WSL2 / a Linux VM — namespace isolation requires a real Linux kernel)
- **Go 1.21+**
- **Python 3.6+** (for the one-time base image setup script)

---

## Getting Started

### 1. Build the binary

```bash
make build
```

Or manually:

```bash
CGO_ENABLED=0 go build -ldflags='-extldflags "-static"' -o docksmith .
```

The result is a fully static binary with no runtime dependencies.

### 2. Import the Alpine base image (one-time, requires internet)

```bash
python3 scripts/setup_base.py
```

This downloads the Alpine Linux 3.18 minirootfs, decompresses it, and imports it into `~/.docksmith/` as `alpine:3.18`. After this, everything works **fully offline**.

---

## Demo Walkthrough

All commands below assume you're in the project root and have completed the setup above.

### Cold build — all cache misses
```bash
./docksmith build -t myapp:latest sample
```
```
Step 1/7 : FROM alpine:3.18
Step 2/7 : WORKDIR /app
Step 3/7 : ENV GREETING=Hello
Step 4/7 : ENV APP_VERSION=1.0
Step 5/7 : COPY . /app [CACHE MISS] 0.00s
Step 6/7 : RUN sh /app/setup.sh [CACHE MISS]
...
Successfully built sha256:35ead2539d95 myapp:latest
```

### Warm rebuild — all cache hits, near-instant, identical digest
```bash
./docksmith build -t myapp:latest sample
```
```
Step 5/7 : COPY . /app [CACHE HIT]
Step 6/7 : RUN sh /app/setup.sh [CACHE HIT]

Successfully built sha256:35ead2539d95 myapp:latest
```

### Partial cache invalidation
Edit any source file and rebuild. Only the affected step and everything below it re-executes:
```bash
echo "# changed" >> sample/run.sh
./docksmith build -t myapp:latest sample
```
```
Step 5/7 : COPY . /app [CACHE MISS] 0.00s   ← source file changed
Step 6/7 : RUN sh /app/setup.sh [CACHE MISS] ← cascades
```

### List images
```bash
./docksmith images
```
```
NAME                 TAG        ID              CREATED
alpine               3.18       279f83be777d    2026-03-11T07:05:01Z
myapp                latest     fc0ad2b6c9db    2026-03-11T07:05:48Z
```

### Run a container
```bash
./docksmith run myapp:latest
```
```
=========================================
Hello, World from Docksmith!
App version : 1.0
=========================================
Build info  : built-on-alpine-3.18
Working dir : /app
Container PID: 1
```

### Override environment variables at runtime
```bash
./docksmith run -e GREETING=Howdy myapp:latest
./docksmith run -e APP_VERSION=9.9 myapp:latest
```

### Verify filesystem isolation (hard pass/fail)
```bash
./docksmith run myapp:latest /bin/sh -c "echo secret > /tmp/hostleak.txt && echo 'wrote file'"
ls /tmp/hostleak.txt
# ls: cannot access '/tmp/hostleak.txt': No such file or directory  ✓
```

Files written inside the container never appear on the host.

### Remove an image
```bash
./docksmith rmi myapp:latest
./docksmith images   # myapp is gone
```

### Skip the cache entirely
```bash
./docksmith build --no-cache -t myapp:latest sample
```

---

## CLI Reference

| Command | Description |
|---|---|
| `docksmith build -t <name:tag> [--no-cache] <context>` | Build an image from a Docksmithfile |
| `docksmith images` | List all images in the local store |
| `docksmith rmi <name:tag>` | Remove an image and its layer files |
| `docksmith run [-e KEY=VALUE] <name:tag> [cmd...]` | Run a container |
| `docksmith import-base <name:tag> <layer.tar>` | Import a raw tar as a base image |

---

## Docksmithfile Syntax

Six instructions are supported. Any other instruction is a hard error.

| Instruction | Behaviour |
|---|---|
| `FROM <image>[:<tag>]` | Load a base image from the local store |
| `WORKDIR <path>` | Set working directory for subsequent instructions |
| `ENV <key>=<value>` | Store an environment variable in the image config |
| `COPY <src> <dest>` | Copy files from the build context into the image. Supports `*` and `**` globs |
| `RUN <command>` | Execute a shell command inside the assembled filesystem — not on the host |
| `CMD ["exec","arg",...]` | Default command when running the container (JSON array form required) |

Only `COPY` and `RUN` produce layers. `WORKDIR`, `ENV`, and `CMD` update the image config only.

### Example Docksmithfile

```dockerfile
FROM alpine:3.18
WORKDIR /app
ENV GREETING=Hello
ENV APP_VERSION=1.0
COPY . /app
RUN sh /app/setup.sh
CMD ["/bin/sh", "/app/run.sh"]
```

---

## How It Works

### State directory

All state lives in `~/.docksmith/`:

```
~/.docksmith/
├── images/    # one JSON manifest per image  (name:tag.json)
├── layers/    # content-addressed tar files  (sha256hex.tar)
└── cache/     # index mapping cache keys to layer digests
```

### Layers

Every `COPY` and `RUN` instruction produces an **immutable delta layer** — a tar archive containing only the files added or modified by that step. Layers are named by the SHA-256 digest of their raw bytes, so identical content is stored exactly once.

Layers are extracted in order when assembling a container filesystem; later layers overwrite earlier ones at the same path.

### Build cache

Before each `COPY` or `RUN`, Docksmith computes a deterministic cache key from:

- Digest of the previous layer (or the base image's manifest digest for the first step)
- Full instruction text as written
- Current `WORKDIR` value
- All accumulated `ENV` values, sorted lexicographically by key
- For `COPY` only: SHA-256 of each source file, sorted by path

A cache hit reuses the stored layer and skips execution. Any miss cascades all subsequent steps to misses. Tar entries are sorted and timestamps zeroed so builds are byte-for-byte reproducible.

### Container isolation

`docksmith run` and `RUN` during build use the **same** isolation primitive:

1. The process re-execs itself inside new `CLONE_NEWPID`, `CLONE_NEWNS`, and `CLONE_NEWUSER` namespaces
2. Inside those namespaces it calls `chroot()` into the assembled rootfs
3. `/proc` is mounted inside the new PID namespace
4. The real command is `exec()`'d, replacing the init process

This means the container process sees a completely different root filesystem with no access to host files, and PID 1 inside the container is the user's command.

---

## Project Structure

```
docksmith/
├── main.go          # CLI entry point + __container_init (chroot/exec)
├── build.go         # Build engine — all 6 instruction handlers
├── cache.go         # Cache key computation + index lookup/store
├── layer.go         # Tar creation, extraction, filesystem snapshot/diff
├── runtime.go       # Linux namespace isolation + rootfs assembly
├── store.go         # Image manifest CRUD + directory layout
├── parser.go        # Docksmithfile parser
├── cmds.go          # CLI command implementations
├── go.mod
├── Makefile
├── scripts/
│   └── setup_base.py   # One-time Alpine 3.18 import
└── sample/
    ├── Docksmithfile
    ├── run.sh           # Container default command
    └── setup.sh         # Run during build by the RUN instruction
```

---

## Out of Scope

Networking, image registries, resource limits, multi-stage builds, bind mounts, detached containers, and daemon processes are all intentionally not implemented.