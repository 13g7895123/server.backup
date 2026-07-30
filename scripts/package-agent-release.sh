#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RELEASES_DIR="${AGENT_RELEASES_DIR:-${ROOT}/artifacts/backup-agent}"
VERSION="${1:-}"

usage() {
  cat <<'EOF'
Usage:
  bash scripts/package-agent-release.sh <version>

Environment variables:
  AGENT_RELEASES_DIR  Output root for release artifacts
  SKIP_BUILD=1        Reuse existing ./backup-agent instead of rebuilding
  BUILD_TIME_UTC      Override manifest built_at in RFC3339 UTC

Output:
  ${AGENT_RELEASES_DIR}/<version>/
    backup-agent-linux-amd64
    backup-agent_<version>_linux_amd64.tar.gz
    backup-agent_<version>_checksums.txt
    install-agent.sh
    diagnose-agent.sh
    backup-agent.service
    manifest.json
EOF
}

if [[ -z "${VERSION}" ]]; then
  usage
  exit 1
fi

if [[ ! "${VERSION}" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "[error] invalid version: ${VERSION}" >&2
  exit 1
fi

require_file() {
  local path="$1"
  if [[ ! -f "${path}" ]]; then
    echo "[error] required file not found: ${path}" >&2
    exit 1
  fi
}

json_escape() {
  local s="$1"
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  s=${s//$'\n'/\\n}
  s=${s//$'\r'/\\r}
  s=${s//$'\t'/\\t}
  printf '%s' "${s}"
}

sha256_file() {
  sha256sum "$1" | awk '{print $1}'
}

size_file() {
  stat -c '%s' "$1"
}

append_checksum() {
  local file_name="$1"
  local file_path="$2"
  printf '%s  %s\n' "$(sha256_file "${file_path}")" "${file_name}" >> "${CHECKSUM_PATH}"
}

verify_linux_amd64_binary() {
  local path="$1"
  if ! command -v file >/dev/null 2>&1; then
    echo "[warn] 'file' not found; skip binary type verification"
    return 0
  fi

  local desc
  desc="$(file -b "${path}")"
  echo "[release] binary check: ${desc}"

  if [[ "${desc}" != *"ELF 64-bit"* ]]; then
    echo "[error] binary is not ELF 64-bit: ${desc}" >&2
    exit 1
  fi
  if [[ "${desc}" != *"x86-64"* ]]; then
    echo "[error] binary is not linux amd64/x86-64: ${desc}" >&2
    exit 1
  fi
  if [[ "${desc}" != *"statically linked"* ]]; then
    echo "[error] binary is not statically linked: ${desc}" >&2
    exit 1
  fi
}

ROOT_BINARY="${ROOT}/backup-agent"
RELEASE_DIR="${RELEASES_DIR}/${VERSION}"
TAR_NAME="backup-agent_${VERSION}_linux_amd64.tar.gz"
PACKAGE_DIR="backup-agent_${VERSION}_linux_amd64"
CHECKSUM_NAME="backup-agent_${VERSION}_checksums.txt"
CHECKSUM_PATH="${RELEASE_DIR}/${CHECKSUM_NAME}"
MANIFEST_PATH="${RELEASE_DIR}/manifest.json"
PACKAGE_WORK_DIR=""

mkdir -p "${RELEASES_DIR}"
if [[ -e "${RELEASE_DIR}" ]]; then
  echo "[error] release already exists: ${RELEASE_DIR}" >&2
  exit 1
fi

cleanup() {
  if [[ -n "${PACKAGE_WORK_DIR}" && -d "${PACKAGE_WORK_DIR}" ]]; then
    rm -rf "${PACKAGE_WORK_DIR}"
  fi
  if [[ -d "${RELEASE_DIR}" ]]; then
    rm -rf "${RELEASE_DIR}"
  fi
}
trap cleanup ERR

if [[ "${SKIP_BUILD:-0}" != "1" ]]; then
  AGENT_VERSION="${VERSION}" bash "${ROOT}/scripts/build-agent.sh"
fi

require_file "${ROOT_BINARY}"
require_file "${ROOT}/scripts/install-agent.sh"
require_file "${ROOT}/scripts/diagnose-agent.sh"
require_file "${ROOT}/scripts/backup-agent.service"

mkdir -p "${RELEASE_DIR}"

cp "${ROOT_BINARY}" "${RELEASE_DIR}/backup-agent-linux-amd64"
chmod 0755 "${RELEASE_DIR}/backup-agent-linux-amd64"
verify_linux_amd64_binary "${RELEASE_DIR}/backup-agent-linux-amd64"

cp "${ROOT}/scripts/install-agent.sh" "${RELEASE_DIR}/install-agent.sh"
cp "${ROOT}/scripts/diagnose-agent.sh" "${RELEASE_DIR}/diagnose-agent.sh"
cp "${ROOT}/scripts/backup-agent.service" "${RELEASE_DIR}/backup-agent.service"
chmod 0755 "${RELEASE_DIR}/install-agent.sh" "${RELEASE_DIR}/diagnose-agent.sh"
chmod 0644 "${RELEASE_DIR}/backup-agent.service"

PACKAGE_WORK_DIR="$(mktemp -d)"
mkdir -p "${PACKAGE_WORK_DIR}/${PACKAGE_DIR}"
cp "${RELEASE_DIR}/backup-agent-linux-amd64" "${PACKAGE_WORK_DIR}/${PACKAGE_DIR}/backup-agent-linux-amd64"
cp "${RELEASE_DIR}/install-agent.sh" "${PACKAGE_WORK_DIR}/${PACKAGE_DIR}/install-agent.sh"
cp "${RELEASE_DIR}/diagnose-agent.sh" "${PACKAGE_WORK_DIR}/${PACKAGE_DIR}/diagnose-agent.sh"
cp "${RELEASE_DIR}/backup-agent.service" "${PACKAGE_WORK_DIR}/${PACKAGE_DIR}/backup-agent.service"
tar -C "${PACKAGE_WORK_DIR}" -czf "${RELEASE_DIR}/${TAR_NAME}" "${PACKAGE_DIR}"
rm -rf "${PACKAGE_WORK_DIR}"
PACKAGE_WORK_DIR=""

COMMIT=""
if command -v git >/dev/null 2>&1; then
  if COMMIT="$(git -C "${ROOT}" rev-parse --short HEAD 2>/dev/null)"; then
    :
  else
    COMMIT=""
  fi
fi

BUILT_AT="${BUILD_TIME_UTC:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

BINARY_SHA="$(sha256_file "${RELEASE_DIR}/backup-agent-linux-amd64")"
BINARY_SIZE="$(size_file "${RELEASE_DIR}/backup-agent-linux-amd64")"
TAR_SHA="$(sha256_file "${RELEASE_DIR}/${TAR_NAME}")"
TAR_SIZE="$(size_file "${RELEASE_DIR}/${TAR_NAME}")"
INSTALL_SHA="$(sha256_file "${RELEASE_DIR}/install-agent.sh")"
INSTALL_SIZE="$(size_file "${RELEASE_DIR}/install-agent.sh")"
DIAGNOSE_SHA="$(sha256_file "${RELEASE_DIR}/diagnose-agent.sh")"
DIAGNOSE_SIZE="$(size_file "${RELEASE_DIR}/diagnose-agent.sh")"
SERVICE_SHA="$(sha256_file "${RELEASE_DIR}/backup-agent.service")"
SERVICE_SIZE="$(size_file "${RELEASE_DIR}/backup-agent.service")"

cat > "${MANIFEST_PATH}" <<EOF
{
  "version": "$(json_escape "${VERSION}")",
  "built_at": "$(json_escape "${BUILT_AT}")",
  "commit": "$(json_escape "${COMMIT}")",
  "files": [
    {
      "name": "backup-agent-linux-amd64",
      "os": "linux",
      "arch": "amd64",
      "size_bytes": ${BINARY_SIZE},
      "sha256": "${BINARY_SHA}"
    },
    {
      "name": "${TAR_NAME}",
      "os": "linux",
      "arch": "amd64",
      "size_bytes": ${TAR_SIZE},
      "sha256": "${TAR_SHA}"
    },
    {
      "name": "install-agent.sh",
      "size_bytes": ${INSTALL_SIZE},
      "sha256": "${INSTALL_SHA}"
    },
    {
      "name": "diagnose-agent.sh",
      "size_bytes": ${DIAGNOSE_SIZE},
      "sha256": "${DIAGNOSE_SHA}"
    },
    {
      "name": "backup-agent.service",
      "size_bytes": ${SERVICE_SIZE},
      "sha256": "${SERVICE_SHA}"
    }
  ]
}
EOF

: > "${CHECKSUM_PATH}"
append_checksum "backup-agent-linux-amd64" "${RELEASE_DIR}/backup-agent-linux-amd64"
append_checksum "${TAR_NAME}" "${RELEASE_DIR}/${TAR_NAME}"
append_checksum "install-agent.sh" "${RELEASE_DIR}/install-agent.sh"
append_checksum "diagnose-agent.sh" "${RELEASE_DIR}/diagnose-agent.sh"
append_checksum "backup-agent.service" "${RELEASE_DIR}/backup-agent.service"
# manifest 會記錄 checksums 檔案的 hash，不能反向再把 manifest 放入 checksums，
# 否則形成循環雜湊，最終必定有一方驗證失敗。

CHECKSUM_SHA="$(sha256_file "${CHECKSUM_PATH}")"
CHECKSUM_SIZE="$(size_file "${CHECKSUM_PATH}")"

cat > "${MANIFEST_PATH}" <<EOF
{
  "version": "$(json_escape "${VERSION}")",
  "built_at": "$(json_escape "${BUILT_AT}")",
  "commit": "$(json_escape "${COMMIT}")",
  "files": [
    {
      "name": "backup-agent-linux-amd64",
      "os": "linux",
      "arch": "amd64",
      "size_bytes": ${BINARY_SIZE},
      "sha256": "${BINARY_SHA}"
    },
    {
      "name": "${TAR_NAME}",
      "os": "linux",
      "arch": "amd64",
      "size_bytes": ${TAR_SIZE},
      "sha256": "${TAR_SHA}"
    },
    {
      "name": "install-agent.sh",
      "size_bytes": ${INSTALL_SIZE},
      "sha256": "${INSTALL_SHA}"
    },
    {
      "name": "diagnose-agent.sh",
      "size_bytes": ${DIAGNOSE_SIZE},
      "sha256": "${DIAGNOSE_SHA}"
    },
    {
      "name": "backup-agent.service",
      "size_bytes": ${SERVICE_SIZE},
      "sha256": "${SERVICE_SHA}"
    },
    {
      "name": "${CHECKSUM_NAME}",
      "size_bytes": ${CHECKSUM_SIZE},
      "sha256": "${CHECKSUM_SHA}"
    }
  ]
}
EOF

echo "[release] ready: ${RELEASE_DIR}"
echo "[release] files:"
find "${RELEASE_DIR}" -maxdepth 1 -type f -printf '  %f\n' | sort
