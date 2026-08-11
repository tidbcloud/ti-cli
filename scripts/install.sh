#!/bin/sh
set -eu

REPO="tidbcloud/ti-cli"
VERSION="latest"
INSTALL_DIR=""
INSTALL_DIR_EXPLICIT=0
DRY_RUN=0
YES=0

BOLD=''
DIM=''
GREEN=''
YELLOW=''
RED=''
RESET=''
if [ -t 1 ]; then
  BOLD="$(printf '\033[1m')"
  DIM="$(printf '\033[2m')"
  GREEN="$(printf '\033[0;32m')"
  YELLOW="$(printf '\033[0;33m')"
  RED="$(printf '\033[0;31m')"
  RESET="$(printf '\033[0m')"
fi

info() { printf "  ${DIM}%s${RESET}\n" "$1"; }
success() { printf "  ${GREEN}%s${RESET}\n" "$1"; }
warn() { printf "  ${YELLOW}%s${RESET}\n" "$1"; }
error() { printf "  ${RED}ti install [ERROR]:${RESET} %s\n" "$1" >&2; exit 1; }

usage() {
  cat <<'USAGE'
Install ti from GitHub Releases.

Usage:
  install.sh [--version latest|v0.2.0] [--install-dir PATH] [--dry-run] [--yes]

Options:
  --version       Release version to install. Defaults to latest.
  --install-dir   Directory that will receive the ti binary. Overrides TI_INSTALL_DIR.
  --dry-run       Print the planned download and install path without changes.
  --yes           Replace an existing binary without prompting.
  --help          Show this help.
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      VERSION="${2:-}"
      shift 2
      ;;
    --install-dir)
      INSTALL_DIR="${2:-}"
      INSTALL_DIR_EXPLICIT=1
      shift 2
      ;;
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    --yes)
      YES=1
      shift
      ;;
    --help)
      usage
      exit 0
      ;;
    *)
      echo "ti install [ERROR]: unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    error "required command not found: $1"
  fi
}

need tar

download() {
  if command -v curl >/dev/null 2>&1; then
    if [ -t 2 ]; then
      curl -fSL --progress-bar -o "$2" "$1"
    else
      curl -fsSL -o "$2" "$1"
    fi
  elif command -v wget >/dev/null 2>&1; then
    if [ -t 2 ]; then
      wget --show-progress -q -O "$2" "$1" 2>&1
    else
      wget -q -O "$2" "$1"
    fi
  else
    error "neither curl nor wget found"
  fi
}

download_quiet_stdout() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1" 2>/dev/null || true
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O - "$1" 2>/dev/null || true
  else
    true
  fi
}

case "$(uname -s)" in
  Darwin) OS="darwin" ;;
  Linux) OS="linux" ;;
  *)
    error "unsupported OS: $(uname -s)"
    ;;
esac

case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *)
    error "unsupported architecture: $(uname -m)"
    ;;
esac

resolve_install_dir() {
  if [ -n "${TI_INSTALL_DIR:-}" ] && [ -n "${TDC_INSTALL_DIR:-}" ] && [ "$TI_INSTALL_DIR" != "$TDC_INSTALL_DIR" ]; then
    error "TI_INSTALL_DIR and deprecated TDC_INSTALL_DIR contain different values"
  fi
  if [ "$INSTALL_DIR_EXPLICIT" -eq 1 ]; then
    [ -n "$INSTALL_DIR" ] || error "--install-dir cannot be empty"
    info "Install dir: ${INSTALL_DIR} (from --install-dir)"
    return
  fi
  if [ -n "${TI_INSTALL_DIR:-}" ]; then
    INSTALL_DIR="$TI_INSTALL_DIR"
    info "Install dir: ${INSTALL_DIR} (from TI_INSTALL_DIR)"
    return
  fi
  if [ -n "${TDC_INSTALL_DIR:-}" ]; then
    INSTALL_DIR="$TDC_INSTALL_DIR"
    info "Install dir: ${INSTALL_DIR} (from deprecated TDC_INSTALL_DIR)"
    return
  fi

  [ -n "${HOME:-}" ] || error "HOME is required when --install-dir and TI_INSTALL_DIR are not set"
  INSTALL_DIR="${HOME}/.ti/bin"
  info "Install dir: ${INSTALL_DIR} (default user install)"
}

run_home_migration() {
  migration_binary="$1"
  if [ -z "${HOME:-}" ]; then
    return
  fi
  info "Checking for state to migrate from ${HOME}/.tdc"
  "$migration_binary" __migrate-home
}

resolve_install_dir

if [ "$VERSION" = "latest" ]; then
  RELEASE_BASE="https://github.com/${REPO}/releases/latest/download"
else
  case "$VERSION" in
    v*) TAG="$VERSION" ;;
    *) TAG="v$VERSION" ;;
  esac
  RELEASE_BASE="https://github.com/${REPO}/releases/download/${TAG}"
fi

ARTIFACT="ti_${OS}_${ARCH}.tar.gz"
ARCHIVE_URL="${RELEASE_BASE}/${ARTIFACT}"
CHECKSUMS_URL="${RELEASE_BASE}/ti_checksums.txt"
TARGET="${INSTALL_DIR}/ti"
COMPANION_ARTIFACT="drive9-${OS}-${ARCH}"
COMPANION_URL="https://drive9.ai/releases/${COMPANION_ARTIFACT}"
COMPANION_CHECKSUMS_URL="https://drive9.ai/releases/checksums.txt"
COMPANION_TARGET="${INSTALL_DIR}/ti-drive9"

if [ "$DRY_RUN" -eq 1 ]; then
  cat <<EOF
ti install dry-run
version: ${VERSION}
artifact: ${ARTIFACT}
archive_url: ${ARCHIVE_URL}
checksums_url: ${CHECKSUMS_URL}
target: ${TARGET}
companion_artifact: ${COMPANION_ARTIFACT}
companion_url: ${COMPANION_URL}
companion_target: ${COMPANION_TARGET}
path_export: export PATH="${INSTALL_DIR}:\$PATH"
EOF
  exit 0
fi

if [ -e "$TARGET" ] && [ "$YES" -ne 1 ] && [ -t 0 ]; then
  printf 'Replace existing %s? [y/N] ' "$TARGET" >&2
  read answer
  case "$answer" in
    y|Y|yes|YES) ;;
    *)
      echo "ti install [ERROR]: cancelled" >&2
      exit 130
      ;;
  esac
fi

bootstrap_config() {
  if [ -z "${HOME:-}" ]; then
    return
  fi
  CONFIG_DIR="${HOME}/.ti"
  CONFIG_FILE="${CONFIG_DIR}/config"
  mkdir -p "$CONFIG_DIR" 2>/dev/null || true
  chmod 700 "$CONFIG_DIR" 2>/dev/null || true
  if [ ! -f "$CONFIG_FILE" ]; then
    cat > "$CONFIG_FILE" <<'CONF'
[default]
region_code = 'aws-us-east-1'
CONF
    chmod 644 "$CONFIG_FILE" 2>/dev/null || true
    info "Bootstrapped ${CONFIG_FILE} with default aws/us-east-1 placement"
  fi
}

install_file() {
  source_path="$1"
  target_path="$2"
  mkdir -p "$INSTALL_DIR" 2>/dev/null || error "cannot create ${INSTALL_DIR}; choose a user-writable directory with --install-dir"
  [ -w "$INSTALL_DIR" ] || error "cannot write to ${INSTALL_DIR}; choose a user-writable directory with --install-dir"
  stage_path="$(mktemp "${INSTALL_DIR}/.ti-install.XXXXXX")" || error "cannot stage installation in ${INSTALL_DIR}"
  cp "$source_path" "$stage_path" || {
    rm -f "$stage_path"
    error "cannot copy binary into ${INSTALL_DIR}"
  }
  chmod 0755 "$stage_path" || {
    rm -f "$stage_path"
    error "cannot make staged binary executable"
  }
  mv -f "$stage_path" "$target_path" || {
    rm -f "$stage_path"
    error "cannot replace ${target_path}"
  }
}

report_path_status() {
  ACTIVE="$(command -v ti 2>/dev/null || true)"
  if [ -n "$ACTIVE" ] && [ "$ACTIVE" != "$TARGET" ]; then
    warn "PATH shadowing detected: ti resolves to ${ACTIVE}"
    warn "Installed binary: ${TARGET}"
  fi
  if [ "$ACTIVE" != "$TARGET" ]; then
    warn "Add ti to the current shell PATH:"
    warn "export PATH=\"${INSTALL_DIR}:\$PATH\""
    warn "Add the same line to your shell profile to persist it"
  fi
}

print_regions() {
  printf "\n"
  printf "  ${BOLD}Config regions:${RESET}\n"
  printf "    aws-us-east-1, aws-us-west-2, aws-eu-central-1, aws-ap-northeast-1, aws-ap-southeast-1\n"
  printf "    ali-ap-southeast-1\n"
  printf "\n"
  printf "  ${BOLD}ti fs regions:${RESET}\n"
  MANIFEST="$(download_quiet_stdout "https://drive9.ai/manifest/regions/drive9-regions.json")"
  FS_REGIONS="$(printf "%s\n" "$MANIFEST" | awk '
    /"mode"[[:space:]]*:[[:space:]]*"tidb_cloud_native"/ { native=1 }
    /"cloud_provider"[[:space:]]*:/ {
      provider=$0
      sub(/^.*"cloud_provider"[[:space:]]*:[[:space:]]*"/, "", provider)
      sub(/".*$/, "", provider)
    }
    /"tidb_region"[[:space:]]*:/ {
      region=$0
      sub(/^.*"tidb_region"[[:space:]]*:[[:space:]]*"/, "", region)
      sub(/".*$/, "", region)
    }
    /^[[:space:]]*}/ {
      if (native && provider != "" && region != "") {
        prefix=provider
        if (prefix == "alicloud" || prefix == "alibaba_cloud") {
          prefix="ali"
        }
        print "    " prefix "-" region
      }
      native=0; provider=""; region=""
    }
  ' | sort -u)"
  if [ -n "$FS_REGIONS" ]; then
    printf "%s\n" "$FS_REGIONS"
  else
    printf "    aws-us-east-1, aws-us-west-2, aws-ap-southeast-1, ali-ap-southeast-1\n"
    warn "Could not fetch the latest ti fs region manifest; run ti fs check-file-system after configure"
  fi
}

print_next_steps() {
  printf "\n"
  printf "  ${BOLD}Get started:${RESET}\n"
  printf "\n"
  printf "    ${BOLD}1.${RESET} Add ti to PATH\n"
  printf "       ${DIM}\$${RESET} export PATH=\"${INSTALL_DIR}:\$PATH\"\n"
  printf "\n"
  printf "    ${BOLD}2.${RESET} Configure credentials\n"
  printf "       ${DIM}\$${RESET} ti configure\n"
  printf "\n"
  printf "    ${BOLD}3.${RESET} List projects\n"
  printf "       ${DIM}\$${RESET} ti organization list-projects --output text\n"
  printf "\n"
  printf "    ${BOLD}4.${RESET} Create or check ti fs\n"
  printf "       ${DIM}\$${RESET} export TI_FS_FILE_SYSTEM_ID=\$(ti fs create-file-system --query file_system_id --output text)\n"
  printf "       ${DIM}\$${RESET} ti fs check-file-system --output text\n"
  printf "\n"
  printf "    ${BOLD}5.${RESET} Mount ti fs when FUSE is available\n"
  printf "       ${DIM}\$${RESET} ti fs mount-file-system --file-system-id \"\$TI_FS_FILE_SYSTEM_ID\" --mount-path ./workspace\n"
  printf "\n"
  printf "  Docs: ${DIM}https://github.com/tidbcloud/ti-cli${RESET}\n"
}

print_telemetry_notice() {
  printf "\n"
  printf "  ${BOLD}Anonymous telemetry:${RESET}\n"
  printf "\n"
  printf "  ti collects anonymous command usage and reliability telemetry in release builds.\n"
  printf "  It collects command and flag names (never values), exit and stable error codes,\n"
  printf "  duration, region, ti version, OS, and architecture.\n"
  printf "\n"
  printf "  It never collects credentials, tokens, SQL text, file paths or contents,\n"
  printf "  command output, API response payloads, or cloud resource IDs.\n"
  printf "\n"
  printf "  To disable telemetry, create or edit ~/.ti/.preferences:\n"
  printf "\n"
  printf "    [telemetry]\n"
  printf "    enabled = false\n"
  printf "\n"
  printf "  For one process: ${DIM}TI_TELEMETRY=off ti ...${RESET}\n"
}

TMP_DIR="$(mktemp -d)"
cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT INT TERM

printf "\n"
printf "  ${BOLD}ti${RESET} installer\n"
printf "  ${DIM}────────────────────────────${RESET}\n"
printf "\n"
info "Platform: ${OS}/${ARCH}"
info "Artifact: ${ARTIFACT}"
info "Companion: ${COMPANION_ARTIFACT}"

download "$ARCHIVE_URL" "${TMP_DIR}/${ARTIFACT}"
download "$CHECKSUMS_URL" "${TMP_DIR}/ti_checksums.txt"
download "$COMPANION_URL" "${TMP_DIR}/${COMPANION_ARTIFACT}"
download "$COMPANION_CHECKSUMS_URL" "${TMP_DIR}/drive9_checksums.txt"

EXPECTED="$(awk -v name="$ARTIFACT" '$2 == name { print $1 }' "${TMP_DIR}/ti_checksums.txt" | head -n 1)"
if [ -z "$EXPECTED" ]; then
  error "checksum for ${ARTIFACT} not found"
fi

if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL="$(sha256sum "${TMP_DIR}/${ARTIFACT}" | awk '{ print $1 }')"
else
  ACTUAL="$(shasum -a 256 "${TMP_DIR}/${ARTIFACT}" | awk '{ print $1 }')"
fi

if [ "$EXPECTED" != "$ACTUAL" ]; then
  error "checksum mismatch for ${ARTIFACT}"
fi

COMPANION_EXPECTED="$(awk -v name="$COMPANION_ARTIFACT" '$2 == name { print $1 }' "${TMP_DIR}/drive9_checksums.txt" | head -n 1)"
if [ -z "$COMPANION_EXPECTED" ]; then
  error "checksum for ${COMPANION_ARTIFACT} not found"
fi

if command -v sha256sum >/dev/null 2>&1; then
  COMPANION_ACTUAL="$(sha256sum "${TMP_DIR}/${COMPANION_ARTIFACT}" | awk '{ print $1 }')"
else
  COMPANION_ACTUAL="$(shasum -a 256 "${TMP_DIR}/${COMPANION_ARTIFACT}" | awk '{ print $1 }')"
fi

if [ "$COMPANION_EXPECTED" != "$COMPANION_ACTUAL" ]; then
  error "checksum mismatch for ${COMPANION_ARTIFACT}"
fi

tar -xzf "${TMP_DIR}/${ARTIFACT}" -C "$TMP_DIR"
FOUND="$(find "$TMP_DIR" -type f -name ti | head -n 1)"
if [ -z "$FOUND" ]; then
  error "archive did not contain ti"
fi
chmod 0755 "$FOUND"
chmod 0755 "${TMP_DIR}/${COMPANION_ARTIFACT}"
run_home_migration "$FOUND"
install_file "$FOUND" "$TARGET"
install_file "${TMP_DIR}/${COMPANION_ARTIFACT}" "$COMPANION_TARGET"

"$TARGET" --version
success "ti installed to ${TARGET}"
success "ti fs companion installed to ${COMPANION_TARGET}"
bootstrap_config
report_path_status
print_regions
print_telemetry_notice
print_next_steps
