#!/bin/bash
set -eu

stress () {
  _start () {
    local network_size
    readonly network_size="${1:?network size required}"

    local running_vals
    readonly running_vals="${2:-$1}"

    >&2 echo 'Compiling...'
    go build -o ./bin/gstress ./cmd/gordian-stress

    local group_id
    readonly group_id="$(./bin/gstress petname).$(date +%s)"
    local socket_path
    readonly socket_path="/tmp/gstress.${group_id}.sock"

    local log_dir
    readonly log_dir="./bin/gstress-run-${group_id}"
    mkdir "$log_dir"

    ./bin/gstress seed "$socket_path" > "${log_dir}/seed.log" 2>&1 &
    ./bin/gstress wait-for-seed "$socket_path"

    >&2 echo 'Starting validators...'
    local i
    for (( i=0; i < "$running_vals"; i++)); do
      ./bin/gstress register-validator "$socket_path" "val-${i}" 1
      ./bin/gstress validator "$socket_path" "val-${i}" > "${log_dir}/val-${i}.log" 2>&1 &
    done

    ./bin/gstress start "$socket_path"

    >&2 printf 'Network started; logs available at: %s ' "${log_dir}/seed.log"
    for (( i=0; i < "$running_vals"; i++)); do
      >&2 printf "${log_dir}/val-${i}.log "
    done
    >&2 echo
    >&2 echo "Watch logs with: tail -f ${log_dir}/val-*.log"
    >&2 echo
    >&2 echo "Stop the network with: ./bin/gstress halt ${socket_path}"
  }

  commands () {
    >&2 cat <<-EOF
stress commands:
  start NETWORK_SIZE [RUNNING_VALS] -> start the stress network
  startr NETWORK_SIZE [RUNNING_VALS] -> start the stress network, with the data race detector enabled
EOF
  }

  local cmd="${1:-}"
  shift || true # If no args, shift would cause the script to fail due to set -e at the top.

  if [ "$cmd" == startr ]; then
    # Data race detection enabled -- just set go environment variables,
    # which will propagate to go build.
    cmd=start
    export GOFLAGS=-race GODEBUG=halt_on_error=1
    >&2 echo 'Data race detection enabled'
  fi

  case "$cmd" in
    start)
      if [ $# -ne 1 ] && [ $# -ne 2 ]; then
        >&2 echo "USAGE: $0 NETWORK_SIZE [RUNNING_VALS]

The NETWORK_SIZE argument determines the number of validators in the network.

RUNNING_VALS defaults to NETWORK_SIZE;
otherwise, if provided, it determines the nuber of validators to run locally.
RUNNING_VALS may be set less than or greater than NETWORK_SIZE."
        exit 1
      fi
      _start "$@"
      ;;
    *)
      commands
      ;;
  esac
}

stress "$@"
