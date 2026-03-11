#!/usr/bin/env python3
"""
setup_base.py
=============
Downloads the Alpine Linux 3.18 minimal root filesystem and imports it into
the Docksmith local image store as 'alpine:3.18'.

Run this once before attempting any builds:
    python3 scripts/setup_base.py

Requirements: Python 3.6+, internet access (one-time only).
"""

import gzip
import hashlib
import json
import os
import subprocess
import sys
import urllib.request

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
ALPINE_VERSION = "3.18.0"
ALPINE_BRANCH  = "v3.18"
ALPINE_ARCH    = "x86_64"
ALPINE_URL     = (
    f"https://dl-cdn.alpinelinux.org/alpine/{ALPINE_BRANCH}/releases/{ALPINE_ARCH}/"
    f"alpine-minirootfs-{ALPINE_VERSION}-{ALPINE_ARCH}.tar.gz"
)

IMAGE_NAME = "alpine"
IMAGE_TAG  = "3.18"

# The Alpine minirootfs ships with a minimal PATH.
ALPINE_ENV = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"


def main():
    # Locate the docksmith binary (same directory as this script's parent).
    script_dir   = os.path.dirname(os.path.realpath(__file__))
    project_root = os.path.dirname(script_dir)
    binary       = os.path.join(project_root, "docksmith")
    if not os.path.isfile(binary):
        # Try current directory.
        binary = os.path.join(os.getcwd(), "docksmith")
    if not os.path.isfile(binary):
        sys.exit(
            "ERROR: docksmith binary not found.\n"
            "Build it first with:  make  (or  go build -o docksmith .)"
        )

    # ------------------------------------------------------------------
    # 1. Download the Alpine minirootfs tarball (.tar.gz).
    # ------------------------------------------------------------------
    print(f"Downloading Alpine Linux {ALPINE_VERSION} minirootfs ...")
    print(f"  URL: {ALPINE_URL}")
    try:
        with urllib.request.urlopen(ALPINE_URL) as resp:
            compressed = resp.read()
    except Exception as exc:
        sys.exit(f"Download failed: {exc}")

    print(f"  Downloaded {len(compressed):,} bytes (gzip)")

    # ------------------------------------------------------------------
    # 2. Decompress: the Docksmith layer store expects raw .tar files.
    # ------------------------------------------------------------------
    print("Decompressing ...")
    raw_tar = gzip.decompress(compressed)
    print(f"  Decompressed to {len(raw_tar):,} bytes (tar)")

    # ------------------------------------------------------------------
    # 3. Write to a temporary file so we can pass it to docksmith.
    # ------------------------------------------------------------------
    import tempfile
    tmp = tempfile.NamedTemporaryFile(suffix=".tar", delete=False)
    try:
        tmp.write(raw_tar)
        tmp.close()

        # ------------------------------------------------------------------
        # 4. Import via `docksmith import-base`.
        # ------------------------------------------------------------------
        cmd = [
            binary,
            "import-base",
            f"{IMAGE_NAME}:{IMAGE_TAG}",
            tmp.name,
            "--cmd", '["/bin/sh"]',
            "--env", ALPINE_ENV,
        ]
        print(f"\nImporting as {IMAGE_NAME}:{IMAGE_TAG} ...")
        result = subprocess.run(cmd, check=False)
        if result.returncode != 0:
            sys.exit("docksmith import-base failed.")
    finally:
        os.unlink(tmp.name)

    # ------------------------------------------------------------------
    # 5. Verify the import succeeded.
    # ------------------------------------------------------------------
    print("\nVerifying import ...")
    result = subprocess.run([binary, "images"], capture_output=True, text=True)
    print(result.stdout)
    if f"{IMAGE_NAME}" not in result.stdout:
        sys.exit("ERROR: alpine image not listed after import.")
    print(f"✓  {IMAGE_NAME}:{IMAGE_TAG} is ready for use in Docksmithfiles.")


if __name__ == "__main__":
    main()