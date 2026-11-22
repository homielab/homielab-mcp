#!/bin/sh
# Check and build mcp-homielab into $BIN_DIR (default $(go env GOPATH)/bin).
# Cross-compile: GOOS=linux GOARCH=arm64 ./build.sh
set -e

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "${ROOT}"
BIN_DIR="${BIN_DIR:-$(go env GOPATH)/bin}"
version="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"

unformatted="$(gofmt -l .)"
if [ -n "${unformatted}" ]; then
    echo "Files need gofmt:" && echo "${unformatted}" && exit 1
fi
go vet ./...
go test ./... > /dev/null

CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${version}" \
    -o "${BIN_DIR}/mcp-homielab" ./cmd/mcp-homielab
echo "built → ${BIN_DIR}/mcp-homielab (version ${version})"
