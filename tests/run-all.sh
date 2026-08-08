#!/usr/bin/env bash
#
# run-all.sh —— Webshell one-click full-test entry.
#
# Chains three steps:
#   1. Build backend binary (backend/bin/webshell) —— integration tests spawn this binary directly.
#   2. Run backend Go unit tests (go test ./...).
#   3. Run Node integration tests (node --test, serial to avoid multi-backend port collisions).
#
# Any step failure means overall failure (non-zero exit). Unattended end-to-end, CI-friendly.
#
# Usage:
#   ./tests/run-all.sh            # run all
#   ./tests/run-all.sh --no-go    # skip Go unit tests (build binary + Node integration only)
#
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKEND_DIR="$ROOT_DIR/backend"
TESTS_DIR="$ROOT_DIR/tests"

RUN_GO=1
for arg in "$@"; do
  case "$arg" in
    --no-go) RUN_GO=0 ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done

# Colors
if [[ -t 1 ]]; then
  GREEN=$'\033[1;32m'; RED=$'\033[1;31m'; BOLD=$'\033[1m'; RESET=$'\033[0m'
else
  GREEN=''; RED=''; BOLD=''; RESET=''
fi

pass=0
fail=0

step() { printf '\n%s== %s ==%s\n' "$BOLD" "$1" "$RESET"; }

# ---------- 1. Build backend binary ----------
step "Build backend binary (go build)"
if ! (cd "$BACKEND_DIR" && go build -o bin/webshell ./cmd/webshell); then
  printf '%sBuild failed%s\n' "$RED" "$RESET" >&2
  exit 1
fi
printf '%s✓ Binary ready:%s\n' "$GREEN" "$RESET"
pass=$((pass + 1))

# ---------- 2. Go unit tests ----------
if [[ "$RUN_GO" -eq 1 ]]; then
  step "Go unit tests (go test ./...)"
  if (cd "$BACKEND_DIR" && go test ./...); then
    printf '%s✓ Go unit tests passed%s\n' "$GREEN" "$RESET"
    pass=$((pass + 1))
  else
    printf '%s✗ Go unit tests failed%s\n' "$RED" "$RESET" >&2
    fail=$((fail + 1))
  fi
fi

# ---------- 3. Node integration tests (serial) ----------
step "Node integration tests (node --test --test-concurrency=1)"
# Ensure dependencies are installed
if [[ ! -d "$TESTS_DIR/node_modules/ws" ]]; then
  printf 'ws dependency missing, running npm install...\n' >&2
  (cd "$TESTS_DIR" && npm install) >&2 || { printf '%snpm install failed%s\n' "$RED" "$RESET" >&2; exit 1; }
fi
if (cd "$TESTS_DIR" && node --test --test-concurrency=1); then
  printf '%s✓ Node integration tests passed%s\n' "$GREEN" "$RESET"
  pass=$((pass + 1))
else
  printf '%s✗ Node integration tests failed%s\n' "$RED" "$RESET" >&2
  fail=$((fail + 1))
fi

# ---------- Summary ----------
step "Summary"
printf 'Passed steps: %s%d%s  Failed steps: %s%d%s\n' \
  "$GREEN" "$pass" "$RESET" \
  "$RED" "$fail" "$RESET"

if [[ "$fail" -gt 0 ]]; then
  printf '%sSome test cases failed%s\n' "$RED" "$RESET" >&2
  exit 1
fi
printf '%sAll tests passed ✅%s\n' "$GREEN" "$RESET"