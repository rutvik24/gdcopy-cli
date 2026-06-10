#!/usr/bin/env bash
set -e

# Repository settings
GITHUB_REPO="rutvik24/gdcopy-cli"
BINARY_NAME="gdcopy"

# Detect OS
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "${OS}" in
  darwin*)  OS="darwin" ;;
  linux*)   OS="linux" ;;
  *)        echo "Unsupported OS: ${OS}"; exit 1 ;;
esac

# Detect Architecture
ARCH="$(uname -m)"
case "${ARCH}" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *)            echo "Unsupported architecture: ${ARCH}"; exit 1 ;;
esac

# Determine installation directory
INSTALL_DIR="/usr/local/bin"
USE_SUDO=""

# Check if the installation directory is writable, otherwise use sudo
if [ ! -w "${INSTALL_DIR}" ]; then
  if [ "$(id -u)" -ne 0 ]; then
    USE_SUDO="sudo"
  fi
fi

# Allow specifying a custom version (e.g., VERSION=v1.0.0). Defaults to latest.
VERSION="${VERSION:-latest}"

if [ "${VERSION}" = "latest" ]; then
  DOWNLOAD_URL="https://github.com/${GITHUB_REPO}/releases/latest/download/gdcopy-${OS}-${ARCH}.tar.gz"
else
  # Ensure tag format starts with 'v' if omitted
  if [[ ! "${VERSION}" =~ ^v ]]; then
    VERSION="v${VERSION}"
  fi
  DOWNLOAD_URL="https://github.com/${GITHUB_REPO}/releases/download/${VERSION}/gdcopy-${OS}-${ARCH}.tar.gz"
fi

echo "Installing ${BINARY_NAME} (${VERSION}) for ${OS}/${ARCH}..."
echo "Downloading from: ${DOWNLOAD_URL}"

# Create a temporary directory
TEMP_DIR=$(mktemp -d)
clean_up() {
  rm -rf "${TEMP_DIR}"
}
trap clean_up EXIT

# Download and extract archive
if ! curl -fsSL "${DOWNLOAD_URL}" -o "${TEMP_DIR}/archive.tar.gz"; then
  echo "Error: Failed to download binary. The release might not exist yet or URL is incorrect."
  exit 1
fi

tar -xzf "${TEMP_DIR}/archive.tar.gz" -C "${TEMP_DIR}"

# Install binary to destination
echo "Installing to ${INSTALL_DIR}..."
${USE_SUDO} mv "${TEMP_DIR}/${BINARY_NAME}" "${INSTALL_DIR}/${BINARY_NAME}"
${USE_SUDO} chmod +x "${INSTALL_DIR}/${BINARY_NAME}"

echo "Successfully installed ${BINARY_NAME} to ${INSTALL_DIR}/${BINARY_NAME}!"
echo "Please restart your shell or run '${BINARY_NAME} -version' to verify."
