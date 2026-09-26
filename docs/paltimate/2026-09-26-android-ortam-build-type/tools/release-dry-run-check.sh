#!/usr/bin/env bash
# After publish.sh's step 2 (the local Test repositories): what step 3 would
# copy into the public tree, and what a consumer resolves from it.
# usage (repo root): release-dry-run-check.sh <version>
set -u
v="$1"
roots=(build/test-repository codegen-gradle/build/test-repository)
dirs="$(find "${roots[@]}" -type d -name "$v" | sed 's|.*/test-repository/||' | sort)"
n="$(grep -c . <<<"$dirs")"
echo "artifact directories for $v: $n"
sed 's/^/  /' <<<"$dirs"
repo=codegen-gradle/build/test-repository/io/palbase
grep -A1 "<artifactId>palbase-codegen-engine</artifactId>" "$repo/codegen-gradle/$v/codegen-gradle-$v.pom" \
  | grep -q "<version>$v</version>" && echo "plugin POM pins palbase-codegen-engine $v"
grep -q "<artifactId>codegen-gradle</artifactId>" \
  "$repo/codegen/io.palbase.codegen.gradle.plugin/$v/io.palbase.codegen.gradle.plugin-$v.pom" \
  && echo "marker io.palbase.codegen $v → io.palbase:codegen-gradle $v"
major="$(unzip -p "$repo/codegen-gradle/$v/codegen-gradle-$v.jar" io/palbase/gradle/PalbaseCodegenPlugin.class \
  | od -An -j6 -N2 -tu1 | awk 'NF {print $1 * 256 + $2}')"
echo "plugin class file major version $major (61 = Java 17)"
[[ "$n" == 11 ]]
