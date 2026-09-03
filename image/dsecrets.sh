#!/bin/sh
# dsecrets NAME[,NAME...] -- command [args...]
# Fetches exactly the named secrets over the Run API and execs the command
# with them in its environment (SPEC §8). Every line is measured; read the
# list under the script in SPEC §8 before changing one.
set -eu

names=${1:?usage: dsecrets NAME[,NAME...] -- cmd}
shift
[ "${1:-}" = "--" ] || { echo "dsecrets: expected -- before the command" >&2; exit 2; }
shift

# --fail-with-body, never -f: a refusal's body names the denied names, and
# -f would discard it. --retry: a 429 is a wait for Retry-After, never a
# denial; a refused attempt's body stays in front of the reply on stdout,
# and every reply of this endpoint is one line, so the last line is it.
nl='
'
resp=$(curl -sS --fail-with-body --retry 3 --max-time 10 \
  -H "Authorization: Bearer $RUN_TOKEN" -H 'Content-Type: application/json' \
  --data "$(jq -cn --arg n "$names" '{names: ($n | split(","))}')" \
  "$CONTROL_PLANE_URL/run/secrets") || refused=$?
# ponytail: holds while every reply is one line; split a -w '%{http_code}'
# off the body if one ever is not.
resp=${resp##*"$nl"}
[ -z "${refused:-}" ] || { echo "dsecrets: request failed${resp:+: $resp}" >&2; exit 3; }

for n in $(echo "$names" | tr ',' ' '); do
  printf '%s' "$resp" | jq -e --arg n "$n" 'has($n)' >/dev/null || {
    echo "dsecrets: control plane did not return: $n" >&2; exit 3; }
  v=$(printf '%s' "$resp" | jq -j --arg n "$n" '.[$n]'; printf X)
  export "$n=${v%X}"
done

exec "$@"
