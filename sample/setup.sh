#!/bin/sh
# setup.sh — executed during the RUN instruction at build time.
# Must not require network access; all files come from the COPY layer.
set -e
echo "Docksmith build: running setup in /app"
echo "Files present:"
ls /app

# Write a build info file into the image.
echo "built-on-alpine-3.18" > /app/.buildinfo
echo "Setup complete."