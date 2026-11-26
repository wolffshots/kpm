#!/bin/sh
# carry-kpm.sh: a kpm for `nkpm-sim --exercise-detailcarry`. The sim is offline,
# so `check <id> --json` cannot reach a forge or build the 6.x bundle. This kpm
# answers it with an update row and marks the bundle as built. After that mark,
# `info <id> --json` reports the package in the bundle, 1.5 s late, so the
# exercise can see whether the page changes before info answers. Each other
# command runs the real kpm in $REAL_KPM.

mark="$KPM_ROOT/.carry-kpm-checked"

case "$1 $3" in
"check --json")
  sleep 0.5
  : > "$mark"
  printf 'BEGIN_JSON{"packages":[{"id":"%s","latest":"v9.9.9","update":true}],"checked":"2026-11-26T00:00:00Z"}\n' "$2"
  ;;
"info --json")
  if [ -f "$mark" ]; then
    sleep 1.5
    "$REAL_KPM" "$@" | sed 's/"carryover":{[^}]*}/"carryover":{"state":"in-bundle","version":"v1.0.0","reason":null}/'
  else
    exec "$REAL_KPM" "$@"
  fi
  ;;
*)
  exec "$REAL_KPM" "$@"
  ;;
esac
