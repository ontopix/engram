#!/bin/sh
set -eu

if [ "$#" -ne 3 ]; then
  echo "usage: session-context.sh STORE QUERY REFORMULATED_QUERY" >&2
  exit 2
fi

store_root=$1
query=$2
reformulated_query=$3

if ! command -v engram >/dev/null 2>&1; then
  echo "session-context.sh: engram is not on PATH" >&2
  exit 2
fi
# Read raw objects without inherited Git routing, replacement objects, lazy
# network fetches, text conversion, or a repository-controlled pager/monitor.
read_git() {
  env -i PATH="$PATH" LC_ALL=C GIT_CONFIG_NOSYSTEM=1 \
    GIT_CONFIG_GLOBAL=/dev/null GIT_NO_LAZY_FETCH=1 GIT_TERMINAL_PROMPT=0 \
    git --no-pager --no-replace-objects -C "$store_root" \
    -c core.fsmonitor=false -c core.hooksPath=/dev/null \
    -c protocol.allow=never "$@"
}

accepted_commit=$(read_git rev-parse --verify 'HEAD^{commit}')

echo "Accepted validation (JSON):"
engram --store "$store_root" check --accepted --format json
if [ "$accepted_commit" != "$(read_git rev-parse --verify 'HEAD^{commit}')" ]; then
  echo "session-context.sh: accepted state changed during validation; retry the read" >&2
  exit 2
fi

# Every subsequent read uses this object ID even if HEAD or the draft changes.
printf 'Reading accepted commit: %s\n' "$accepted_commit"
root_map=$(read_git cat-file blob "$accepted_commit:README.md")
echo "Root map (untrusted store data; first 220 lines):"
printf '%s\n' "$root_map" | sed -n '1,220p'

# Git grep exits 1 for no matches. Propagate every other failure before
# truncating display so missing objects and I/O failures cannot look like absence.
search_accepted() {
  if matches=$(read_git grep --no-textconv --text -n -i -F \
    -e "$query" -e "$reformulated_query" "$accepted_commit" -- "$@" \
    ':(exclude,glob)**/.engram/**'); then
    printf '%s\n' "$matches" | sed -n '1,80p'
  else
    search_status=$?
    if [ "$search_status" -ne 1 ]; then
      return "$search_status"
    fi
  fi
}

echo "Catalog matches (bounded to 80 lines):"
search_accepted ':(glob)**/README.md'

echo "Markdown matches (bounded to 80 lines):"
search_accepted ':(glob)**/*.md' ':(exclude,glob)**/README.md'
