#!/usr/bin/env bash
# After scripts/publish.sh: the public Maven tree serves every coordinate a
# consumer resolves, at <version>. BASE points elsewhere for a dry run.
# usage: post-publish-check.sh <version>
set -u
v="$1"
base="${BASE:-https://palgroup.github.io/palbackend-android}"
fail=0
for coordinate in palbe palbe-core palbe-integrity palbe-notifications palbe-messaging palbe-purchases \
  palbe-call palbe-debug-ui palbase-codegen-engine codegen-gradle codegen/io.palbase.codegen.gradle.plugin; do
  name="${coordinate##*/}"
  if curl -sf -o /dev/null "$base/io/palbase/$coordinate/$v/$name-$v.pom"; then
    echo "ok    $coordinate $v"
  else
    echo "FAIL  $coordinate $v — $base/io/palbase/$coordinate/$v/$name-$v.pom"; fail=1
  fi
done
exit "$fail"
