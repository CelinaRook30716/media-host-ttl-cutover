#!/bin/sh
set -eu

: "${INFRAI_API_KEY:?set INFRAI_API_KEY}"
go run ./cmd/media-cutover
