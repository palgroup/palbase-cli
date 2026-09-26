#!/usr/bin/env bash
# The notes scripts/publish.sh attaches to the GitHub Release of $1 — its own
# awk line — and whether they carry the whole 2.4 list.
version="$1"
notes="$(awk -v h="## $version" 'index($0,h)==1{p=1;next} p&&/^## /{exit} p' CHANGELOG.md)"
echo "notes for $version: $(wc -l <<<"$notes" | tr -d ' ') lines, $(grep -c 'DAVRANIŞ DEĞİŞİKLİĞİ' <<<"$notes") DAVRANIŞ DEĞİŞİKLİĞİ heading(s)"
grep -q 'DAVRANIŞ DEĞİŞİKLİĞİ' <<<"$notes" || { echo "publish.sh would attach no 2.4 notes for $version"; exit 1; }
"$(dirname "$0")/changelog-check.sh" "## $version"
