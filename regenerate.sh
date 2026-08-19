#!/usr/bin/env bash
set -e

./stop.sh 2>/dev/null || true

cp -a world/datapacks /tmp/datapacks

rm -rf world
mkdir -p world

cp -a /tmp/datapacks world/

./start.sh
