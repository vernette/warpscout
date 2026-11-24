#!/usr/bin/env sh

set -e

REPO="vernette/warpscout"
BIN_NAME="warpscout"
API_URL="https://api.github.com/repos/${REPO}/releases/latest"

ASSUME_YES=0
WANT_VERSION=""

if [ -e /etc/openwrt_release ]; then
  DEFAULT_DIR="/usr/bin"
elif [ -n "${TERMUX_VERSION}" ]; then
  DEFAULT_DIR="${PREFIX}/bin"
else
  DEFAULT_DIR="${HOME}/.local/bin"
fi
INSTALL_DIR="${INSTALL_DIR:-$DEFAULT_DIR}"

for c in curl wget; do
  if type "$c" >/dev/null 2>&1; then
    DOWNLOADER="$c"
    break
  fi
done

# fetch <url> <destination file, or - for stdout>
fetch() {
  if [ "$DOWNLOADER" = "curl" ]; then
    if [ "$2" = "-" ]; then
      "$DOWNLOADER" -fsSL "$1"
    else
      "$DOWNLOADER" -fsSL -o "$2" "$1"
    fi
    return
  fi
  "$DOWNLOADER" -qO "$2" "$1"
}

usage() {
  cat <<EOF
Install ${BIN_NAME} into ${INSTALL_DIR}.

Usage:
  install.sh [options]

Options:
  --version <ver>  install this release instead of the latest (v0.9.0 or 0.9.0)
  --uninstall      remove an installed ${BIN_NAME} and exit
  -y, --yes        do not ask before replacing an installed ${BIN_NAME}
  -h, --help       show this help

Environment:
  INSTALL_DIR      where to put the binary (default ${DEFAULT_DIR})
EOF
}

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
    --version)
      [ $# -ge 2 ] || die "--version needs a value."
      WANT_VERSION="${2#v}"
      shift 2
      ;;
    --uninstall)
      uninstall
      ;;
    -y | --yes)
      ASSUME_YES=1
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      die "Unknown option: $1"
      ;;
    esac
  done
}

die() {
  echo "Error: $1" >&2
  exit 1
}

uninstall() {
  if [ ! -e "${INSTALL_DIR}/${BIN_NAME}" ]; then
    echo "Nothing to remove: ${INSTALL_DIR}/${BIN_NAME} does not exist."
    exit 0
  fi
  rm -f "${INSTALL_DIR}/${BIN_NAME}"
  echo "Removed ${INSTALL_DIR}/${BIN_NAME}"
  exit 0
}

mips_arch() {
  case "$(dd if=/bin/sh bs=1 skip=5 count=1 2>/dev/null | od -An -tu1 | tr -d ' ')" in
  1) ARCH="mipsle" ;;
  2) ARCH="mips" ;;
  *) die "Cannot detect the byte order of this MIPS CPU." ;;
  esac
}

detect_platform() {
  # Termux reports itself as Linux under uname -s, only -o says Android.
  case "$(uname -o 2>/dev/null || uname -s)" in
  Android) OS="android" ;;
  Linux | GNU/Linux) OS="linux" ;;
  Darwin) OS="darwin" ;;
  *) die "Unsupported OS: $(uname -s). Windows builds are on the releases page." ;;
  esac

  case "$(uname -m)" in
  x86_64 | amd64) ARCH="amd64" ;;
  aarch64 | arm64) ARCH="arm64" ;;
  mips) mips_arch ;;
  *) die "Unsupported architecture: $(uname -m)." ;;
  esac
}

resolve_version() {
  if [ -n "$WANT_VERSION" ]; then
    VERSION="$WANT_VERSION"
    return
  fi
  VERSION="$(fetch "$API_URL" - | grep -o '"tag_name": *"[^"]*"' | head -n 1 | cut -d'"' -f4)"
  VERSION="${VERSION#v}"
  [ -n "$VERSION" ] || die "Failed to resolve the latest release of ${REPO}."
}

check_installed() {
  [ -x "${INSTALL_DIR}/${BIN_NAME}" ] || return 0
  CURRENT="$("${INSTALL_DIR}/${BIN_NAME}" version 2>/dev/null || echo "unknown")"

  if [ "$CURRENT" = "$VERSION" ]; then
    echo "${BIN_NAME} ${VERSION} is already installed in ${INSTALL_DIR}."
    exit 0
  fi
  confirm_update
}

confirm_update() {
  headline="Update available"
  if [ -n "$WANT_VERSION" ]; then
    headline="Requested version differs"
  fi
  printf "%s: %s %s -> %s (in %s)\n" \
    "$headline" "$BIN_NAME" "$CURRENT" "$VERSION" "$INSTALL_DIR"

  if [ "$ASSUME_YES" = 1 ] || ! (: </dev/tty) 2>/dev/null; then
    return 0
  fi
  printf "Install %s now? [Y/n] " "$VERSION"
  read -r answer </dev/tty
  case "$answer" in
  "" | y | Y | yes | YES) return 0 ;;
  *)
    echo "Keeping ${BIN_NAME} ${CURRENT}."
    exit 0
    ;;
  esac
}

cleanup() {
  [ -n "${TMP_DIR:-}" ] && rm -rf "$TMP_DIR"
}

download_and_install() {
  ASSET="${BIN_NAME}_${VERSION}_${OS}_${ARCH}.tar.gz"
  ASSET_URL="https://github.com/${REPO}/releases/download/v${VERSION}/${ASSET}"

  TMP_DIR="$(mktemp -d)"
  trap cleanup EXIT INT TERM

  echo "Downloading ${ASSET_URL}"
  fetch "$ASSET_URL" "${TMP_DIR}/${ASSET}" || die "Failed to download ${ASSET_URL}."
  tar xzf "${TMP_DIR}/${ASSET}" -C "$TMP_DIR" || die "Failed to unpack ${ASSET}."

  mkdir -p "$INSTALL_DIR"
  cp "${TMP_DIR}/${BIN_NAME}" "${INSTALL_DIR}/${BIN_NAME}"
  chmod 755 "${INSTALL_DIR}/${BIN_NAME}"
}

check_quarantine() {
  [ "$OS" = "darwin" ] || return 0
  printf "\n%s\n  %s\n" \
    "macOS may quarantine the binary. If it refuses to run:" \
    "xattr -d com.apple.quarantine \"${INSTALL_DIR}/${BIN_NAME}\""
}

check_path() {
  case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) return 0 ;;
  esac
  printf "\n%s\n  %s\n" \
    "${INSTALL_DIR} is not in your PATH. Add it:" \
    "echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.profile"
}

main() {
  parse_args "$@"
  [ -n "$DOWNLOADER" ] || die "Need curl or wget."
  detect_platform
  resolve_version
  check_installed
  download_and_install
  printf "%s\n%s\n" "Installed ${BIN_NAME} ${VERSION} to ${INSTALL_DIR}"
  check_quarantine
  check_path
}

main "$@"
