#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"

"${SCRIPT_DIR}/_clean_issues.sh" "$ROOT_DIR"
"${SCRIPT_DIR}/_clean_specs.sh" "$ROOT_DIR"
