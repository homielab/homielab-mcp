#!/bin/sh
# Install the latest mcp-homielab release (macOS and Linux).
#
#   curl -fsSL https://raw.githubusercontent.com/homielab/homielab-mcp/main/install.sh | sh
#
# Env: MCP_HOMIELAB_VERSION=v0.1.0 (default: latest),
#      MCP_HOMIELAB_INSTALL_DIR=/usr/local/bin (default: ~/.local/bin).
set -eu

REPO="homielab/homielab-mcp"
BIN="mcp-homielab"
INSTALL_DIR="${MCP_HOMIELAB_INSTALL_DIR:-${HOME}/.local/bin}"

case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) echo "Unsupported OS $(uname -s). On Windows, download the zip from https://github.com/${REPO}/releases" >&2; exit 1 ;;
esac
case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) echo "Unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac

if [ -n "${MCP_HOMIELAB_VERSION:-}" ]; then
    base="https://github.com/${REPO}/releases/download/${MCP_HOMIELAB_VERSION}"
else
    base="https://github.com/${REPO}/releases/latest/download"
fi
archive="${BIN}_${os}_${arch}.tar.gz"

tmp="$(mktemp -d)"
trap 'rm -r "${tmp}"' EXIT

echo "Downloading ${archive}..."
curl -fsSL -o "${tmp}/${archive}" "${base}/${archive}"
curl -fsSL -o "${tmp}/checksums.txt" "${base}/checksums.txt"

expected="$(grep " ${archive}\$" "${tmp}/checksums.txt" | cut -d' ' -f1)"
if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "${tmp}/${archive}" | cut -d' ' -f1)"
else
    actual="$(shasum -a 256 "${tmp}/${archive}" | cut -d' ' -f1)"
fi
if [ -z "${expected}" ] || [ "${expected}" != "${actual}" ]; then
    echo "Checksum mismatch for ${archive}" >&2
    exit 1
fi

tar -xzf "${tmp}/${archive}" -C "${tmp}" "${BIN}"
mkdir -p "${INSTALL_DIR}"
mv "${tmp}/${BIN}" "${INSTALL_DIR}/${BIN}"
chmod 755 "${INSTALL_DIR}/${BIN}"

echo "Installed ${INSTALL_DIR}/${BIN} ($("${INSTALL_DIR}/${BIN}" version))"
case ":${PATH}:" in
    *":${INSTALL_DIR}:"*) ;;
    *) echo "Add ${INSTALL_DIR} to your PATH, e.g.: echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.zshrc" ;;
esac
echo
echo "Next: ${BIN} add all        # register every service with Claude Code"
echo "      ${BIN}                # list services and their setup commands"
