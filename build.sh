#!/bin/sh
# Check and build every MCP server in the repo (one Go module).
# Each mcp-*/ dir becomes a binary of the same name in $BIN_DIR
# (default $(go env GOPATH)/bin).
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

for dir in mcp-*/; do
    name="$(basename "${dir}")"
    CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w -X main.version=${version}" \
        -o "${BIN_DIR}/${name}" "./${name}"
    echo "built → ${BIN_DIR}/${name} (version ${version})"
done
