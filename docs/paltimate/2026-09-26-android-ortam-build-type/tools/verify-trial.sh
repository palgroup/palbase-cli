#!/usr/bin/env bash
# FR-301 acceptance for palbe-trial-android, run at its root. Arguments go to
# Gradle as they are (the scratch proof passes an init script there).
set -u
fail=0
check() { if eval "$2"; then echo "ok    $1"; else echo "FAIL  $1"; fail=1; fi; }
check "the plugin is pinned at 2.5.0"   'grep -qF "id(\"io.palbase.codegen\") version \"2.5.0\"" build.gradle.kts'
check "palbe is pinned at 2.5.0"        'grep -qF "io.palbase:palbe:2.5.0" app/build.gradle.kts'
check "no palbase { } block"            '! grep -q "^palbase {" app/build.gradle.kts'
check "no global palbase.env"           '! grep -q "^palbase.env=" gradle.properties'
check "no palbase/.gitattributes"       '! test -e palbase/.gitattributes'
check "no roles.json"                   '! test -e palbase/environments/main/roles.json'
rm -rf app/build/outputs/apk   # an APK an earlier build left must not answer for this one
out="$(./gradlew --console=plain "$@" :app:generatePalbaseDebug --rerun :app:generatePalbaseRelease --rerun \
  :app:assembleDebug :app:assembleRelease 2>&1)"; code=$?
check "debug and release build"         '[ "$code" = 0 ]'
check "debug → main, from its own key"  'grep -qF "Palbase: debug → main (palbase.env.debug in gradle.properties) [palbase/environments]" <<<"$out"'
check "release → main, from its own key" 'grep -qF "Palbase: release → main (palbase.env.release in gradle.properties) [palbase/environments]" <<<"$out"'
for apk in app/build/outputs/apk/debug/app-debug.apk app/build/outputs/apk/release/app-release-unsigned.apk; do
  check "$(basename "$apk") packs main" 'unzip -p "$apk" assets/palbase/palbase-config.json 2>/dev/null | grep -qF "\"palbase_environment\":\"main\""'
done
grep -E "^Palbase: |FAILED$|^e: |What went wrong" <<<"$out" | head -8 | sed 's/^/  | /'
exit "$fail"
