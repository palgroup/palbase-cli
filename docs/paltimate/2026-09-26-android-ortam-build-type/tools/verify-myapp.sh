#!/usr/bin/env bash
# FR-302 acceptance for MyApplicationPalbaseAndroidSdkTest — the spec's
# "Hedef son durum" — run at its root. Arguments go to Gradle as they are.
# With PERSONAL=1 it checks the personal local.properties line instead.
set -u
fail=0
check() { if eval "$2"; then echo "ok    $1"; else echo "FAIL  $1"; fail=1; fi; }
apk() { unzip -p "app/build/outputs/apk/$1/app-$1$2.apk" assets/palbase/palbase-config.json 2>/dev/null; }
if [[ "${PERSONAL:-}" == 1 ]]; then
  check "local.properties is ignored by git" 'grep -qxE "/?local.properties" .gitignore'
  check "local.properties aims debug at featureX" 'grep -qx "palbase.env.debug=featureX" local.properties'
  rm -rf app/build/outputs/apk   # an APK an earlier build left must not answer for this one
  out="$(./gradlew --console=plain "$@" :app:generatePalbaseDebug --rerun :app:assembleDebug 2>&1)"; code=$?
  check "debug builds" '[ "$code" = 0 ]'
  check "debug → featureX, from local.properties" 'grep -qF "Palbase: debug → featureX (palbase.env.debug in local.properties) [palbase/environments]" <<<"$out"'
  check "app-debug.apk packs featureX" 'apk debug "" | grep -qF "\"palbase_environment\":\"featureX\""'
  grep -E "^Palbase: |FAILED$|^e: |What went wrong" <<<"$out" | head -8 | sed 's/^/  | /'
  exit "$fail"
fi
check "the plugin is pinned at 2.4.0"       'grep -qF "id(\"io.palbase.codegen\") version \"2.4.0\"" app/build.gradle.kts'
check "palbe is pinned at 2.4.0"            'grep -qx "palbe = \"2.4.0\"" gradle/libs.versions.toml'
check "no project-level palbase { } block"  '! grep -q "^palbase {" app/build.gradle.kts'
check "the build type DSL is imported"      'grep -qx "import io.palbase.gradle.palbase" app/build.gradle.kts'
check "no global palbase.env"               '! grep -q "^palbase.env=" gradle.properties'
check "debug and release keys committed"    'grep -qx "palbase.env.debug=main" gradle.properties && grep -qx "palbase.env.release=main" gradle.properties'
check "no config copies under app/src"      '[ -z "$(find app/src -name android-config.json -o -name openapi.json)" ]'
for env in main featureX featureY feature-profile-update; do
  check "palbase/environments/$env is linked" 'test -f "palbase/environments/$env/android-config.json" -a -f "palbase/environments/$env/openapi.json"'
done
rm -rf app/build/outputs/apk   # an APK an earlier build left must not answer for this one
out="$(./gradlew --console=plain "$@" \
  :app:generatePalbaseDebug --rerun :app:generatePalbaseRelease --rerun :app:generatePalbaseFeatureX --rerun \
  :app:generatePalbaseFeatureY --rerun :app:generatePalbaseFeatureProfileUpdate --rerun \
  :app:assembleDebug :app:assembleRelease :app:assembleFeatureX :app:assembleFeatureY :app:assembleFeatureProfileUpdate 2>&1)"; code=$?
check "every variant builds" '[ "$code" = 0 ]'
while IFS='|' read -r variant apkname env line; do
  check "$variant → $env" 'grep -qF "$line" <<<"$out"'
  check "the $variant APK packs $env" 'apk "$variant" "$apkname" | grep -qF "\"palbase_environment\":\"$env\""'
done <<'TABLE'
debug||main|Palbase: debug → main (palbase.env.debug in gradle.properties) [palbase/environments]
release|-unsigned|main|Palbase: release → main (palbase.env.release in gradle.properties) [palbase/environments]
featureX||featureX|Palbase: featureX → featureX (from the build type name) [palbase/environments]
featureY||featureY|Palbase: featureY → featureY (from the build type name) [palbase/environments]
featureProfileUpdate||feature-profile-update|Palbase: featureProfileUpdate → feature-profile-update (palbase { environment } in the `featureProfileUpdate` build type) [palbase/environments]
TABLE
grep -E "^Palbase: |FAILED$|^e: |What went wrong" <<<"$out" | head -12 | sed 's/^/  | /'
exit "$fail"
