#!/bin/bash
set -Eeuo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
MODE=${1:-smoke}
case "$MODE" in
  preflight|ollama|lmstudio|smoke|full) ;;
  *) echo "usage: $0 {preflight|ollama|lmstudio|smoke|full}" >&2; exit 2 ;;
esac

OLLAMA_MODEL=${RCODEGEN_E2E_OLLAMA_MODEL:-qwen3.5:4b}
LM_MODEL=${RCODEGEN_E2E_LMSTUDIO_MODEL:-gemma-4-31b-it-abliterated}
LM_CONTEXT=${RCODEGEN_E2E_LMSTUDIO_CONTEXT:-2048}
MIN_FREE_PERCENT=${RCODEGEN_E2E_MIN_FREE_PERCENT:-50}
MAX_OLLAMA_GIB=${RCODEGEN_E2E_MAX_OLLAMA_GIB:-8}
MAX_LM_DISK_GIB=${RCODEGEN_E2E_MAX_LM_DISK_GIB:-24}
MAX_LM_ESTIMATE_GIB=${RCODEGEN_E2E_MAX_LM_ESTIMATE_GIB:-20}
GRPC_PORT=${RCODEGEN_E2E_RSERVE_PORT:-18260}
HTTP_PORT=$((GRPC_PORT + 1))
GRPC_ADDR="127.0.0.1:${GRPC_PORT}"
HTTP_BASE="http://127.0.0.1:${HTTP_PORT}"
OLLAMA_BASE=${RCODEGEN_E2E_OLLAMA_BASE_URL:-http://127.0.0.1:11434}
LM_BASE=${RCODEGEN_E2E_LMSTUDIO_BASE_URL:-http://127.0.0.1:1234}
PROFILE=smoke
if [[ "$MODE" == full || "$MODE" == ollama || "$MODE" == lmstudio ]]; then PROFILE=full; fi

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/rcodegen-localai-e2e.XXXXXX")
TEST_HOME="$WORK_DIR/home"
mkdir -p "$TEST_HOME"
LOCK_DIR="${TMPDIR:-/tmp}/rcodegen-localai-e2e.lock"
LOCK_OWNED=0
RSERVE_PID=""
LM_SERVER_STARTED=0
OLLAMA_OWNED=0
LM_OWNED=0
LM_ALIAS="rcodegen-e2e-$$"

acquire_lock() {
  if mkdir "$LOCK_DIR" 2>/dev/null; then
    echo $$ >"$LOCK_DIR/pid"
    LOCK_OWNED=1
    return
  fi
  local owner=""
  owner=$(cat "$LOCK_DIR/pid" 2>/dev/null || true)
  if [[ -n "$owner" ]] && kill -0 "$owner" 2>/dev/null; then
    echo "another local-runtime E2E run is active (pid $owner)" >&2
    exit 1
  fi
  rm -rf "$LOCK_DIR"
  mkdir "$LOCK_DIR"
  echo $$ >"$LOCK_DIR/pid"
  LOCK_OWNED=1
}

stop_rserve() {
  [[ -z "$RSERVE_PID" ]] && return 0
  if kill -0 "$RSERVE_PID" 2>/dev/null; then
    kill -TERM "$RSERVE_PID" 2>/dev/null || true
    local i
    for i in $(seq 1 50); do
      kill -0 "$RSERVE_PID" 2>/dev/null || break
      sleep 0.1
    done
    if kill -0 "$RSERVE_PID" 2>/dev/null; then kill -KILL "$RSERVE_PID" 2>/dev/null || true; fi
    wait "$RSERVE_PID" 2>/dev/null || true
  fi
  RSERVE_PID=""
}

ollama_names() {
  curl -fsS --max-time 3 "$OLLAMA_BASE/api/ps" 2>/dev/null | python3 -c 'import json,sys; print("\n".join((m.get("name") or m.get("model") or "") for m in json.load(sys.stdin).get("models", [])))'
}

lm_loaded_json() {
  lms ps --json 2>/dev/null
}

lm_alias_present() {
  LM_ALIAS="$LM_ALIAS" lm_loaded_json | LM_ALIAS="$LM_ALIAS" python3 -c 'import json,sys,os; a=os.environ["LM_ALIAS"]; raise SystemExit(0 if any(a in json.dumps(x) for x in json.load(sys.stdin)) else 1)'
}

unload_owned_ollama() {
  [[ "$OLLAMA_OWNED" -eq 0 ]] && return 0
  ollama stop "$OLLAMA_MODEL" >/dev/null 2>&1 || true
  local i names
  for i in $(seq 1 60); do
    names=$(ollama_names || true)
    if ! printf '%s\n' "$names" | grep -Fxq "$OLLAMA_MODEL"; then
      OLLAMA_OWNED=0
      return 0
    fi
    sleep 0.5
  done
  echo "failed to unload owned Ollama model $OLLAMA_MODEL" >&2
  return 1
}

unload_owned_lm() {
  [[ "$LM_OWNED" -eq 0 ]] && return 0
  lms unload "$LM_ALIAS" >/dev/null 2>&1 || true
  local i
  for i in $(seq 1 60); do
    if ! lm_alias_present 2>/dev/null; then
      LM_OWNED=0
      return 0
    fi
    sleep 0.5
  done
  echo "failed to unload owned LM Studio instance $LM_ALIAS" >&2
  return 1
}

cleanup() {
  local status=${1:-1}
  trap - EXIT INT TERM HUP
  set +e
  if [[ "$LOCK_OWNED" -eq 0 ]]; then
    rm -rf "$WORK_DIR"
    exit "$status"
  fi
  stop_rserve
  unload_owned_ollama || status=1
  unload_owned_lm || status=1
  local remaining_ollama remaining_lm lm_empty=0
  if ! remaining_ollama=$(ollama_names); then
    echo "could not verify Ollama state after E2E cleanup" >&2
    status=1
  elif [[ -n "$remaining_ollama" ]]; then
    echo "Ollama still has loaded models after E2E cleanup: $remaining_ollama" >&2
    status=1
  fi
  if ! remaining_lm=$(lm_loaded_json); then
    echo "could not verify LM Studio state after E2E cleanup" >&2
    status=1
  elif ! printf '%s' "$remaining_lm" | python3 -c 'import json,sys; raise SystemExit(0 if len(json.load(sys.stdin)) == 0 else 1)'; then
    echo "LM Studio still has loaded models after E2E cleanup: $remaining_lm" >&2
    status=1
  else
    lm_empty=1
  fi
  if [[ "$LM_SERVER_STARTED" -eq 1 && "$lm_empty" -eq 1 ]]; then
    if ! lms server stop >/dev/null 2>&1; then
      echo "failed to restore LM Studio server to its original stopped state" >&2
      status=1
    fi
  elif [[ "$LM_SERVER_STARTED" -eq 1 ]]; then
    echo "leaving LM Studio server running to avoid unloading a model not owned by this E2E run" >&2
  fi
  rm -rf "$WORK_DIR"
  if [[ "$(cat "$LOCK_DIR/pid" 2>/dev/null)" == "$$" ]]; then rm -rf "$LOCK_DIR"; fi
  exit "$status"
}

assert_empty() {
  local ollama="" lm=""
  if ! ollama=$(ollama_names); then
    echo "refusing to continue: cannot verify Ollama loaded-model state at $OLLAMA_BASE" >&2
    return 1
  fi
  if ! lm=$(lm_loaded_json); then
    echo "refusing to continue: cannot verify LM Studio loaded-model state" >&2
    return 1
  fi
  if [[ -n "$ollama" ]]; then
    echo "refusing to continue: Ollama already has loaded models:" >&2
    printf '%s\n' "$ollama" >&2
    return 1
  fi
  if ! printf '%s' "$lm" | python3 -c 'import json,sys; raise SystemExit(0 if len(json.load(sys.stdin)) == 0 else 1)'; then
    echo "refusing to continue: LM Studio already has loaded models: $lm" >&2
    return 1
  fi
}

assert_lm_alias_only() {
  local loaded
  loaded=$(lm_loaded_json)
  LM_ALIAS="$LM_ALIAS" python3 - "$loaded" <<'PY'
import json,os,sys
d=json.loads(sys.argv[1]); alias=os.environ['LM_ALIAS']
if len(d) != 1 or alias not in json.dumps(d[0]):
    raise SystemExit(f'LM Studio loaded state is not the single owned alias {alias!r}: {d!r}')
PY
}

assert_ports_free() {
  GRPC_PORT="$GRPC_PORT" HTTP_PORT="$HTTP_PORT" python3 - <<'PY'
import os, socket
for raw in (os.environ["GRPC_PORT"], os.environ["HTTP_PORT"]):
    port=int(raw)
    s=socket.socket()
    try:
        s.bind(("127.0.0.1", port))
    except OSError as e:
        raise SystemExit(f"required E2E port {port} is unavailable: {e}")
    finally:
        s.close()
PY
}

check_memory_pressure() {
  local free
  free=$(memory_pressure -Q 2>/dev/null | sed -n 's/.*System-wide memory free percentage: \([0-9][0-9]*\)%.*/\1/p')
  if [[ -z "$free" || "$free" -lt "$MIN_FREE_PERCENT" ]]; then
    echo "memory guard refused model load: free=${free:-unknown}% required=${MIN_FREE_PERCENT}%" >&2
    return 1
  fi
  echo "memory free: ${free}% (minimum ${MIN_FREE_PERCENT}%)"
}

check_ollama_model() {
  local payload="$WORK_DIR/ollama-tags.json"
  curl -fsS --max-time 5 "$OLLAMA_BASE/api/tags" >"$payload"
  OLLAMA_MODEL="$OLLAMA_MODEL" MAX_GIB="$MAX_OLLAMA_GIB" python3 - "$payload" <<'PY'
import json, os, sys
d=json.load(open(sys.argv[1]))
name=os.environ['OLLAMA_MODEL']; limit=float(os.environ['MAX_GIB'])*(1024**3)
matches=[m for m in d.get('models',[]) if (m.get('name') or m.get('model')) == name]
if not matches: raise SystemExit(f'Ollama model {name!r} is not installed')
size=int(matches[0].get('size') or 0)
if size <= 0 or size > limit: raise SystemExit(f'Ollama model {name!r} size {size/(1024**3):.2f} GiB exceeds guard {limit/(1024**3):.2f} GiB')
print(f'Ollama model: {name} ({size/(1024**3):.2f} GiB)')
PY
}

check_lm_model() {
  local payload="$WORK_DIR/lms-models.json"
  lms ls --json >"$payload"
  LM_MODEL="$LM_MODEL" MAX_GIB="$MAX_LM_DISK_GIB" python3 - "$payload" <<'PY'
import json, os, sys
d=json.load(open(sys.argv[1])); name=os.environ['LM_MODEL']; limit=float(os.environ['MAX_GIB'])*(1024**3)
def matches(m): return m.get('modelKey') == name or name in (m.get('variants') or []) or m.get('indexedModelIdentifier') == name
found=[m for m in d if matches(m) and m.get('type') == 'llm']
if not found: raise SystemExit(f'LM Studio LLM {name!r} is not installed')
size=int(found[0].get('sizeBytes') or 0)
if size <= 0 or size > limit: raise SystemExit(f'LM Studio model {name!r} size {size/(1024**3):.2f} GiB exceeds disk guard {limit/(1024**3):.2f} GiB')
print(f'LM Studio model: {name} ({size/(1024**3):.2f} GiB on disk)')
PY
}

check_lm_estimate() {
  local raw="$WORK_DIR/lms-estimate.raw" clean="$WORK_DIR/lms-estimate.txt"
  lms load "$LM_MODEL" --estimate-only --context-length "$LM_CONTEXT" -y >"$raw" 2>&1
  python3 - "$raw" "$clean" <<'PY'
import re,sys
s=open(sys.argv[1]).read(); s=re.sub(r'\x1b\[[0-9;]*m','',s); open(sys.argv[2],'w').write(s); print(s,end='')
PY
  MAX_GIB="$MAX_LM_ESTIMATE_GIB" python3 - "$clean" <<'PY'
import os,re,sys
s=open(sys.argv[1]).read(); m=re.search(r'Estimated Total Memory:\s*([0-9.]+)\s*GiB',s)
if not m: raise SystemExit('could not parse LM Studio memory estimate')
estimate=float(m.group(1)); limit=float(os.environ['MAX_GIB'])
if estimate > limit: raise SystemExit(f'LM Studio estimate {estimate:.2f} GiB exceeds guard {limit:.2f} GiB')
PY
}

ensure_lm_server() {
  if lms server status 2>&1 | grep -qi 'is running'; then return 0; fi
  lms server start >/dev/null
  LM_SERVER_STARTED=1
  local i
  for i in $(seq 1 40); do
    curl -fsS --max-time 1 "$LM_BASE/v1/models" >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  echo "LM Studio server did not become ready" >&2
  return 1
}

start_rserve() {
  local provider=$1 model=$2
  local log="$WORK_DIR/rserve-${provider}.log"
  assert_ports_free
  local ollama_url="$OLLAMA_BASE" lm_url="$LM_BASE" ollama_model="" lm_model=""
  if [[ "$provider" == ollama ]]; then
    ollama_model="$model"; lm_url="http://127.0.0.1:1"
  else
    lm_model="$model"; ollama_url="http://127.0.0.1:1"
  fi
  env HOME="$TEST_HOME" RSERVE_TOKEN= \
    RCODEGEN_OLLAMA_BASE_URL="$ollama_url" RCODEGEN_OLLAMA_MODEL="$ollama_model" \
    RCODEGEN_LMSTUDIO_BASE_URL="$lm_url" RCODEGEN_LMSTUDIO_MODEL="$lm_model" \
    "$ROOT/bin/rserve" -port "$GRPC_PORT" >"$log" 2>&1 &
  RSERVE_PID=$!
  echo "$RSERVE_PID" >"$WORK_DIR/rserve.pid"
  local i
  for i in $(seq 1 80); do
    if curl -fsS --max-time 1 "$HTTP_BASE/health" >/dev/null 2>&1; then return 0; fi
    if ! kill -0 "$RSERVE_PID" 2>/dev/null; then cat "$log" >&2; return 1; fi
    sleep 0.1
  done
  cat "$log" >&2
  echo "rserve did not become ready" >&2
  return 1
}

run_e2e_test() {
  local provider=$1 model=$2 stage=$3 available=$4
  local ollama_url="$OLLAMA_BASE" lm_url="$LM_BASE" ollama_model="" lm_model=""
  if [[ "$provider" == ollama ]]; then ollama_model="$model"; else lm_model="$model"; fi
  env RCODEGEN_E2E_LOCALAI=1 RCODEGEN_E2E_PROVIDER="$provider" RCODEGEN_E2E_MODEL="$model" \
    RCODEGEN_E2E_STAGE="$stage" RCODEGEN_E2E_PROFILE="$PROFILE" RCODEGEN_E2E_EXPECT_AVAILABLE="$available" \
    RCODEGEN_E2E_HTTP_BASE="$HTTP_BASE" RCODEGEN_E2E_GRPC_ADDR="$GRPC_ADDR" \
    RCODEGEN_E2E_RBATCH="$ROOT/bin/rbatch" RCODEGEN_E2E_TEST_HOME="$TEST_HOME" \
    RCODEGEN_OLLAMA_BASE_URL="$ollama_url" RCODEGEN_OLLAMA_MODEL="$ollama_model" \
    RCODEGEN_LMSTUDIO_BASE_URL="$lm_url" RCODEGEN_LMSTUDIO_MODEL="$lm_model" \
    go test -tags=localai_e2e -count=1 -p=1 -v ./e2e/localai -run '^TestLocalRuntimeE2E$'
}

run_ollama_phase() {
  echo "== Ollama phase =="
  check_memory_pressure
  check_ollama_model
  start_rserve ollama "$OLLAMA_MODEL"
  run_e2e_test ollama "$OLLAMA_MODEL" inventory true
  assert_empty
  [[ "$MODE" == preflight ]] && { stop_rserve; return; }
  OLLAMA_OWNED=1
  run_e2e_test ollama "$OLLAMA_MODEL" runtime true
  stop_rserve
  unload_owned_ollama
  assert_empty
}

run_lm_phase() {
  echo "== LM Studio phase =="
  check_memory_pressure
  check_lm_model
  ensure_lm_server
  check_lm_estimate
  start_rserve lmstudio "$LM_ALIAS"
  run_e2e_test lmstudio "$LM_ALIAS" inventory false
  assert_empty
  [[ "$MODE" == preflight ]] && { stop_rserve; return; }
  LM_OWNED=1
  lms load "$LM_MODEL" --identifier "$LM_ALIAS" --context-length "$LM_CONTEXT" --parallel 1 --ttl 60 -y
  assert_lm_alias_only
  assert_empty_ollama=$(ollama_names || true)
  if [[ -n "$assert_empty_ollama" ]]; then echo "Ollama became loaded during LM Studio phase" >&2; return 1; fi
  run_e2e_test lmstudio "$LM_ALIAS" runtime true
  stop_rserve
  unload_owned_lm
  assert_empty
}

trap 'cleanup $?' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
acquire_lock
assert_empty

case "$MODE" in
  preflight) run_ollama_phase; run_lm_phase ;;
  ollama) run_ollama_phase ;;
  lmstudio) run_lm_phase ;;
  smoke|full) run_ollama_phase; run_lm_phase ;;
esac

assert_empty
echo "local-runtime E2E $MODE completed with both runtimes empty"
