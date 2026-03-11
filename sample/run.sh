#!/bin/sh
# run.sh — executed as the container's default command
echo "========================================="
echo "${GREETING}, World from Docksmith!"
echo "App version : ${APP_VERSION}"
echo "========================================="
echo "Build info  : $(cat /app/.buildinfo 2>/dev/null || echo 'unknown')"
echo "Working dir : $(pwd)"
echo "Container PID: $$"
echo "Files in /app:"
ls /app
echo "========================================="# changed
