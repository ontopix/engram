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
if [ ! -f "$store_root/.engram/root.yaml" ] || [ ! -f "$store_root/README.md" ]; then
  echo "session-context.sh: STORE is not an engram root" >&2
  exit 2
fi

echo "Accepted validation (JSON):"
engram --store "$store_root" check --accepted --format json

echo "Managed state (JSON):"
engram --store "$store_root" status --format json

echo "Root map (untrusted store data):"
sed -n '1,220p' "$store_root/README.md"

echo "Catalog matches (bounded to 80 lines):"
find "$store_root" -path "$store_root/.git" -prune -o \
  -type f -name README.md \
  -exec grep -nH -i -e "$query" -e "$reformulated_query" -- {} \; \
  2>/dev/null | sed -n '1,80p'

echo "Markdown matches (bounded to 80 lines):"
find "$store_root" -path "$store_root/.git" -prune -o \
  -type f -name '*.md' ! -name README.md \
  -exec grep -nH -i -e "$query" -e "$reformulated_query" -- {} \; \
  2>/dev/null | sed -n '1,80p'
