package backend

// app_environments.go — an app holds EVERY environment, and the build picks one.
//
// The old model was one environment at a time: `palbase ios use staging`
// overwrote the config, so the app in your hands was whichever environment
// somebody linked last. Two consequences, both of which happened. A developer
// running against staging could not glance at production without re-linking and
// re-generating; and a TestFlight build could carry a staging address because a
// `use` ran before the archive.
//
// So the link downloads them all, the plist carries them all, and the BUILD
// CONFIGURATION decides. That is the one place in an Xcode project where a
// decision is already made per-build and cannot be forgotten at run time: Debug
// builds `local`, a TestFlight scheme builds `staging`, Release builds `main`.
//
// Each environment gets its own generated client, because environments serve
// different contracts — dev is usually ahead — and a client merged across them
// would compile calls that do not exist where the app is pointed. The xcconfig
// excludes the others, so exactly one is in the build.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// appEnvironment is one environment as an app needs it.
type appEnvironment struct {
	AppID   string `json:"app_id"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	// SealedRoot is the public half of the root this stack's sealing chain hangs
	// from. An app cannot derive it and must not fetch it anonymously — a root
	// taken from the server it is meant to authenticate proves nothing — so it
	// travels here, written once by a `link` the operator ran against their own
	// stack.
	//
	// omitempty: a stack with no chain leaves it out, and the SDK then keeps its
	// compiled-in roots. Writing "" would look like a configured root.
	SealedRoot string          `json:"sealed_root,omitempty"`
	OAuth      json.RawMessage `json:"oauth,omitempty"`
}

// appEnvironments is what the CLI writes and the generator reads.
type appEnvironments struct {
	// Default is the environment a build with no PALBASE_ENV setting gets. It
	// is the safe one — production — because a build that forgot to say which
	// environment it wanted must not silently talk to a developer's laptop.
	Default      string                    `json:"default_environment"`
	Environments map[string]appEnvironment `json:"environments"`
}

// localEnvName is the environment every app checkout gets for free: the stack
// running on this machine.
const localEnvName = "local"

// generatedDir is where the per-environment clients live, committed.
const generatedDir = "Palbase/Generated"

func (a appEnvironments) names() []string {
	out := make([]string, 0, len(a.Environments))
	for name := range a.Environments {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// writeAppEnvironments records every environment for one platform.
//
// BİLMEDİĞİNİ SİLMEZ. Bu dosyayı iki yol yazıyor: bulut yolu (uygulamayı ve
// ortamlarını düzlemden çözer) ve yığın-URL yolu (`palbase link <url>`).
// İkincisi uygulamanın OAuth yapılandırmasını GÖREMEZ — o bilgi düzlemde
// yaşıyor — ve dosyayı olduğu gibi yazınca onu siliyordu.
//
// Ölçüldü 25.08.2026, centauri: bir `link <url>` koşumu `app_id`'yi yığın
// ref'iyle değiştirdi, `api_key`'i düşürdü ve Apple+Google bloğunu TAMAMEN
// sildi. Uygulama derlenmeye devam ederdi; kaybolan tek şey giriş olurdu, ve
// bunu hiçbir hata söylemezdi.
//
// Bu dosyanın kendi kuralı zaten yazılıydı — kaybolan bir Local girdisi için:
// *"an app whose Local configuration disappears ... stops compiling for a
// reason nobody connects to the container"*. Aynı gerekçe ALANLAR için de
// geçerli; bu, o kuralın alan seviyesindeki hâli.
func writeAppEnvironments(platform string, envs appEnvironments) (string, error) {
	dir := filepath.Join(nativeArtifactsDir, platform)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	envs = mergeWithExisting(dir, envs)
	blob, err := json.MarshalIndent(envs, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "palbase-config.json")
	if err := os.WriteFile(path, append(blob, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// mergeWithExisting, yazacağı şeyin ÜRETEMEDİĞİ alanları diskteki dosyadan
// korur.
//
// Yalnız BOŞ alanlar doldurulur: yeni koşum bir değer ürettiyse o kazanır —
// aksi hâlde bu birleştirme, güncellenmesi gereken bir anahtarı sonsuza kadar
// eski tutardı. Dosya okunamıyorsa (ilk link, bozuk JSON) yeni hâl olduğu gibi
// yazılır: birleştirilecek bir şey yoktur.
// isUnsetAppID, değerin gerçek bir uygulama kaydı OLMADIĞINI söyler.
func isUnsetAppID(v string) bool {
	v = strings.TrimSpace(v)
	return v == "" || v == projectAppID
}

func mergeWithExisting(dir string, next appEnvironments) appEnvironments {
	raw, err := os.ReadFile(filepath.Join(dir, "palbase-config.json"))
	if err != nil {
		return next
	}
	var prev appEnvironments
	if err := json.Unmarshal(raw, &prev); err != nil {
		return next
	}
	for name, env := range next.Environments {
		old, ok := prev.Environments[name]
		if !ok {
			continue
		}
		// OAUTH BULUT YOLUNUN BİLDİĞİ BİR ŞEY. Yığın-URL yolu onu hiç kurmuyor
		// ve sildiğinde uygulamanın Apple/Google girişi sessizce ölürdü.
		if len(env.OAuth) == 0 {
			env.OAuth = old.OAuth
		}
		// ANAHTAR VE UYGULAMA KİMLİĞİ de aynı kural: üretilemeyen bir değer,
		// var olanı silmek için gerekçe değildir.
		if strings.TrimSpace(env.APIKey) == "" {
			env.APIKey = old.APIKey
		}
		// YER TUTUCU DA "ÜRETİLMEMİŞ"TİR. Yığın-URL yolu `projectAppID` yazıyor
		// ve bu kod tabanının KENDİSİ onu gerçek bir kimlik saymıyor:
		// `native_link.go` uygulamanın kaydını ararken `e.AppID != projectAppID`
		// diye açıkça atlıyor. Gerçek bir kaydı onunla ezmek, bir kimliği
		// anlamsız bir sabitle değiştirmek olurdu.
		if isUnsetAppID(env.AppID) && !isUnsetAppID(old.AppID) {
			env.AppID = old.AppID
		}
		next.Environments[name] = env
	}
	// DİSKTEKİ FAZLA ORTAMLAR DA KALIR: bir koşumun göremediği ortam (kapalı
	// bir yerel yığın, erişilemeyen bir düzlem) kaybolmamalı.
	for name, old := range prev.Environments {
		if _, ok := next.Environments[name]; !ok {
			next.Environments[name] = old
		}
	}
	return next
}

// writeWebArtifacts writes the two committed files @palbase/web's `palbe-gen`
// reads, in the shape it actually reads them.
//
// THE WEB SHAPE IS NOT THE NATIVE SHAPE, and the difference is not cosmetic.
// A native slot is a MAP of environments (`{default_environment, environments:
// {...}}`) because one app binary is built against several; the web config is
// FLAT (`{app_id, base_url, api_key}`) because a deployed web app is one
// environment — `readWebConfig` in palbe/src/gen/generate.ts requires those
// three fields at the top level and reads nothing else.
//
// Writing the native document here (which this path did until 2026-08-25)
// produces a file with none of the three required fields. `palbe-gen` then
// refuses it, and nothing upstream reports a failure — every step of the link
// succeeded.
//
// The contract goes in beside it: the native path commits one spec per
// environment under `.palbase/openapi/`, and `palbe-gen` reads exactly one,
// `Palbase/openapi.json`.
func writeWebArtifacts(envs appEnvironments, specs map[string][]byte) (string, error) {
	env, ok := envs.Environments[envs.Default]
	if !ok {
		return "", fmt.Errorf("internal: no %q environment to write the web config from", envs.Default)
	}
	if err := os.MkdirAll(webArtifactsDir, 0o755); err != nil {
		return "", err
	}
	cfg := map[string]any{
		"app_id":   env.AppID,
		"base_url": env.BaseURL,
		"api_key":  env.APIKey,
	}
	if len(env.OAuth) > 0 {
		cfg["oauth"] = env.OAuth
	}
	raw, err := json.MarshalIndent(mergeWebConfigWithExisting(cfg), "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(webArtifactsDir, "palbase-config.json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return "", err
	}
	if spec, ok := specs[envs.Default]; ok {
		if err := os.WriteFile(filepath.Join(webArtifactsDir, "openapi.json"), spec, 0o644); err != nil {
			return "", err
		}
	}
	return path, nil
}

// mergeWebConfigWithExisting is mergeWithExisting's rule for the flat document:
// A WRITER MUST NOT DELETE WHAT IT CANNOT PRODUCE.
//
// The cloud path knows things this one does not — `kind`, the OAuth block — and
// they live in the same file. A run that overwrote it wholesale would take the
// app's Apple/Google sign-in with it, and the app would go on building.
func mergeWebConfigWithExisting(next map[string]any) map[string]any {
	raw, err := os.ReadFile(filepath.Join(webArtifactsDir, "palbase-config.json"))
	if err != nil {
		return next
	}
	var prev map[string]any
	if err := json.Unmarshal(raw, &prev); err != nil {
		return next
	}
	// A native document that an earlier run of THIS path left here is not a web
	// config; there is nothing in it to preserve.
	if _, isNativeShape := prev["environments"]; isNativeShape {
		return next
	}
	out := map[string]any{}
	for k, v := range prev {
		// PRESERVING WHAT A WRITER CANNOT PRODUCE IS NOT PRESERVING WHAT THE
		// CONTRACT DELETED. `environment_ref` was taken out on purpose — the
		// identity comes from the key and from nowhere else, and a copy that
		// must equal its original is not a second fact but a second chance to
		// be wrong. The cloud writer already drops it (it rewrites the document
		// whole); merging carried it forward, so a re-link onto a NEW project
		// left the file naming the OLD environment (measured 2026-08-25,
		// palai-cloud: `"environment_ref": "palaicloudm"` survived a re-link to
		// a project called something else entirely).
		if k == removedEnvironmentRefField {
			continue
		}
		out[k] = v
	}
	for k, v := range next {
		if str, isStr := v.(string); isStr && strings.TrimSpace(str) == "" {
			continue
		}
		// The placeholder is "not produced" too — the same rule the native
		// merge applies, and for the same reason: overwriting a real
		// registration with a constant replaces an identity with nothing.
		if k == "app_id" {
			if str, _ := v.(string); isUnsetAppID(str) {
				if old, _ := prev["app_id"].(string); !isUnsetAppID(old) {
					continue
				}
			}
		}
		out[k] = v
	}
	return out
}

// removedEnvironmentRefField is a field this contract no longer has. It is named
// here so a writer can refuse to carry it forward rather than merely not emit it.
const removedEnvironmentRefField = "environment_ref"

// specPath is where one environment's contract is committed.
func specPath(env string) string {
	return filepath.Join(nativeArtifactsDir, "openapi", env+".json")
}

// writeXcconfigs writes one build configuration per environment.
//
// Two settings each, and both are load-bearing. PALBASE_ENV names the
// environment and reaches the app through its Info.plist, which is where the SDK
// reads it — a build setting alone would be invisible at run time. The exclusion
// list keeps the OTHER environments' generated clients out of the compile, so
// "which endpoints exist" is decided by the same configuration that decides
// which address they are called at.
func writeXcconfigs(root string, envs appEnvironments, w io.Writer) error {
	dir := filepath.Join(root, "Palbase", "Config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	names := envs.names()
	for _, name := range names {
		var excluded []string
		for _, other := range names {
			if other != name {
				excluded = append(excluded, fmt.Sprintf("*/%s/%s/*", filepath.Base(generatedDir), other))
			}
		}
		body := fmt.Sprintf(`// GENERATED by palbase link — do not edit.
//
// Build with this configuration and the app talks to the %q environment: the SDK
// reads PALBASE_ENV from the Info.plist, and only this environment's generated
// client is compiled.

PALBASE_ENV = %s
INFOPLIST_KEY_PALBASE_ENV = $(PALBASE_ENV)
EXCLUDED_SOURCE_FILE_NAMES = $(inherited) %s
`, name, name, strings.Join(excluded, " "))

		path := filepath.Join(dir, xcconfigName(name))
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(w, "wrote %s\n", path)
	}
	return nil
}

// xcconfigName is the file an Xcode configuration points at. Capitalised because
// that is how build configurations are named in every Xcode project ever
// created, and a file called `local.xcconfig` next to a configuration called
// `Local` reads as a different thing.
func xcconfigName(env string) string {
	if env == "" {
		return "Palbase.xcconfig"
	}
	return strings.ToUpper(env[:1]) + env[1:] + ".xcconfig"
}

// reportContractDrift says which endpoints one environment has and another does
// not.
//
// This is the question an app developer actually asks — "can I build this
// feature against production yet?" — and the answer is otherwise a compile error
// in a configuration they were not building at the time.
func reportContractDrift(specs map[string][]byte, w io.Writer) {
	if len(specs) < 2 {
		return
	}
	routes := map[string]map[string]bool{}
	for env, spec := range specs {
		routes[env] = pathsOf(spec)
	}

	names := make([]string, 0, len(routes))
	for env := range routes {
		names = append(names, env)
	}
	sort.Strings(names)

	// Compared against the DEFAULT environment rather than pairwise: production
	// is the baseline everything is eventually held to, and n² lines of
	// difference is a report nobody reads.
	base := names[0]
	if _, ok := routes[localEnvName]; ok && len(names) > 1 {
		// …unless one of them is the local stack, in which case the interesting
		// question is what the developer has that the deployed ones do not.
		for _, n := range names {
			if n != localEnvName {
				base = n
				break
			}
		}
	}

	for _, env := range names {
		if env == base {
			continue
		}
		var only, missing []string
		for path := range routes[env] {
			if !routes[base][path] {
				only = append(only, path)
			}
		}
		for path := range routes[base] {
			if !routes[env][path] {
				missing = append(missing, path)
			}
		}
		if len(only) == 0 && len(missing) == 0 {
			continue
		}
		sort.Strings(only)
		sort.Strings(missing)
		fmt.Fprintf(w, "\n%s and %s serve different contracts:\n", env, base)
		for _, path := range only {
			fmt.Fprintf(w, "  only in %s:  %s\n", env, path)
		}
		for _, path := range missing {
			fmt.Fprintf(w, "  not in %s:   %s\n", env, path)
		}
	}
}

// pathsOf reads an OpenAPI document's operations as "METHOD /path" strings.
func pathsOf(spec []byte) map[string]bool {
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	out := map[string]bool{}
	if json.Unmarshal(spec, &doc) != nil {
		return out
	}
	for path, methods := range doc.Paths {
		for method := range methods {
			switch strings.ToLower(method) {
			case "get", "post", "put", "patch", "delete", "query":
				out[strings.ToUpper(method)+" "+path] = true
			}
		}
	}
	return out
}

// gatherEnvironments collects every environment this app can be built against:
// the linked project, and the stack running on this machine.
//
// The local one is added WITHOUT a key when the stack is not up, rather than
// left out: an app whose Local configuration disappears because a container was
// stopped is an app that stops compiling for a reason nobody connects to the
// container. The entry says what to run instead.
func gatherEnvironments(ctx context.Context, target Target, key string, w io.Writer) (appEnvironments, map[string][]byte, error) {
	envs := appEnvironments{
		Default:      defaultEnvName(target),
		Environments: map[string]appEnvironment{},
	}
	specs := map[string][]byte{}

	primary := defaultEnvName(target)
	primaryEnv := appEnvironment{
		AppID:   projectAppID,
		BaseURL: target.URL,
		APIKey:  key,
	}
	// Best effort, and deliberately not fatal: a stack that cannot answer about
	// its sealing root is a stack that seals nothing, which is a legal state and
	// not a reason to refuse the link an operator asked for.
	if _, root, err := projectKeys(ctx, target); err == nil && root != "" {
		primaryEnv.SealedRoot = root
	}
	envs.Environments[primary] = primaryEnv
	cred, _, err := Credential(target.URL)
	if err != nil {
		return appEnvironments{}, nil, err
	}
	spec, err := fetchStackSpec(ctx, target, cred)
	if err != nil {
		return appEnvironments{}, nil, err
	}
	if err := writeSpec(primary, spec); err != nil {
		return appEnvironments{}, nil, err
	}
	specs[primary] = spec

	// The stack on this machine, when there is one and it is not already the
	// target.
	localURL := LookupLocalStack(groupOf(target))
	if localURL == "" || localURL == target.URL {
		return envs, specs, nil
	}

	localTarget := Target{URL: localURL, Local: true}
	localCred, _, credErr := Credential(localURL)
	if credErr != nil {
		envs.Environments[localEnvName] = appEnvironment{AppID: projectAppID, BaseURL: localURL}
		fmt.Fprintf(w, "local: %s is registered but this machine holds no credential for it — `palbase start`\n", localURL)
		return envs, specs, nil
	}
	localKey, keyErr := projectPublishableKey(ctx, localTarget)
	if keyErr != nil {
		// FR-057: the entry is written keyless, and the sequence that fills it
		// is named. A missing entry would be a build configuration that vanishes.
		envs.Environments[localEnvName] = appEnvironment{AppID: projectAppID, BaseURL: localURL}
		fmt.Fprintf(w, "local: %s did not answer — run `palbase start`, then `palbase spec` to fill it in\n", localURL)
		return envs, specs, nil
	}
	localEnv := appEnvironment{
		AppID:   projectAppID,
		BaseURL: localURL,
		APIKey:  localKey,
	}
	if _, root, err := projectKeys(ctx, localTarget); err == nil && root != "" {
		localEnv.SealedRoot = root
	}
	envs.Environments[localEnvName] = localEnv
	if localSpec, err := fetchStackSpec(ctx, localTarget, localCred); err == nil {
		if err := writeSpec(localEnvName, localSpec); err != nil {
			return appEnvironments{}, nil, err
		}
		specs[localEnvName] = localSpec
	}
	return envs, specs, nil
}

// defaultEnvName is what the linked target's environment is called. A cloud
// project names it; a project you run yourself is its own single environment,
// and `main` is what every other surface calls that one.
func defaultEnvName(target Target) string {
	if target.Env != "" {
		return target.Env
	}
	return "main"
}

// groupOf is the project group a target belongs to, for finding its local stack
// in the machine register.
func groupOf(target Target) string {
	if target.Project != "" {
		return target.Project
	}
	root, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Base(root)
}

func writeSpec(env string, spec []byte) error {
	path := specPath(env)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, spec, 0o644)
}

// generateForEnvironments emits one client per environment, and one plist for
// all of them.
func generateForEnvironments(ctx context.Context, envs appEnvironments, w io.Writer) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	// THE OLD FLAT CLIENT GOES FIRST.
	//
	// One environment used to mean one Palbase/Generated/PalbaseGenerated.swift.
	// Writing the per-environment ones beside it leaves BOTH in the target —
	// Xcode 16's synchronized groups compile every file under the folder — and
	// the app stops building at all: "Multiple commands produce
	// PalbaseGenerated.stringsdata". Measured on the real todoapp app, which
	// failed immediately after a relink and built again the moment the old file
	// was deleted by hand.
	//
	// Deleted rather than left for the person to find: it is OUR file, it is
	// regenerated content, and its only remaining effect is to break the build.
	legacy := filepath.Join(root, generatedDir, "PalbaseGenerated.swift")
	if err := os.Remove(legacy); err == nil {
		fmt.Fprintf(w, "removed %s (one client per environment now)\n", legacy)
	} else if !os.IsNotExist(err) {
		return err
	}

	tool, err := ensureSwiftgenTool(root, w)
	if err != nil {
		// SPEC REFRESHED, GENERATOR UNAVAILABLE. On a genuine first link there is
		// nothing generated and this is a note; on a RE-link the clients on disk
		// were emitted from the previous contract and they still COMPILE, so the
		// drift stays invisible until a call 404s on a device. Every one of them
		// goes, and the command fails loudly.
		stale := []string{filepath.Join(root, generatedDir, "Palbase-Info.plist")}
		for _, env := range envs.names() {
			stale = append(stale, filepath.Join(root, generatedDir, env, "PalbaseGenerated.swift"))
		}
		return discardStaleGenerated(err, w, stale...)
	}

	// The two halves are requested separately: one client per environment, and
	// the plist ONCE. The generator accepts either half alone, which is what
	// makes that possible — asking for the plist alongside every client would
	// write the same bytes N times and read as though the environment mattered
	// to it, when the plist is built from the config files and nothing else.
	for _, env := range envs.names() {
		spec := specPath(env)
		if !isRegularFile(spec) {
			// An environment whose contract has not been fetched — the local
			// stack while it is down. Its plist entry exists; its client cannot,
			// and inventing an empty one would compile and then 404.
			continue
		}
		outDir := filepath.Join(root, generatedDir, env)
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return err
		}
		out := filepath.Join(outDir, "PalbaseGenerated.swift")
		cmd := exec.CommandContext(ctx, tool, "--openapi", spec, "--out-swift", out)
		cmd.Stderr = w
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("palbase-swiftgen (%s): %w", env, err)
		}
		fmt.Fprintf(w, "✓ wrote %s\n", out)
	}

	var configFlags []string
	for _, platform := range []string{"ios", "macos"} {
		cfg := filepath.Join(nativeArtifactsDir, platform, "palbase-config.json")
		if isRegularFile(cfg) {
			configFlags = append(configFlags, "--"+platform+"-config", cfg)
		}
	}
	if len(configFlags) == 0 {
		return nil // no Apple slot in this checkout
	}
	plist := filepath.Join(root, generatedDir, "Palbase-Info.plist")
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, tool, append([]string{"--out-plist", plist}, configFlags...)...)
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("palbase-swiftgen (plist): %w", err)
	}
	fmt.Fprintf(w, "✓ wrote %s (%s)\n", plist, strings.Join(envs.names(), ", "))
	return nil
}

// readAppEnvironments reads back what the link wrote for one platform.
func readAppEnvironments(platform string) (appEnvironments, error) {
	dir := filepath.Join(nativeArtifactsDir, platform)
	raw, err := os.ReadFile(filepath.Join(dir, "palbase-config.json"))
	if os.IsNotExist(err) {
		return appEnvironments{}, nil
	}
	if err != nil {
		return appEnvironments{}, err
	}
	var envs appEnvironments
	if err := json.Unmarshal(raw, &envs); err != nil {
		return appEnvironments{}, fmt.Errorf("read the app's environments: %w", err)
	}
	return envs, nil
}

// reportInfoPlistRequirement says the one thing an xcconfig cannot do by itself.
//
// The xcconfig sets PALBASE_ENV and INFOPLIST_KEY_PALBASE_ENV, and the second is
// how the value was meant to reach the app. Xcode merges INFOPLIST_KEY_* only
// into a plist it GENERATES (GENERATE_INFOPLIST_FILE = YES); a target with an
// explicit Info.plist gets the setting computed and thrown away. Measured on a
// real simulator: a build in the Local configuration signed up against the MAIN
// environment's address while every build setting still read `local`.
//
// That is the worst shape a failure can take here — the app talks to production
// while everything on screen says otherwise — so it is reported at the moment
// the configurations are written, with the exact line to add.
func reportInfoPlistRequirement(root string, envs appEnvironments, w io.Writer) {
	plists := appInfoPlists(root)
	if len(plists) == 0 {
		// A target whose plist Xcode generates: INFOPLIST_KEY_PALBASE_ENV does
		// reach it, and there is nothing to add.
		return
	}
	var missing []string
	for _, path := range plists {
		body, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(body), "PALBASE_ENV") {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			missing = append(missing, rel)
		}
	}
	if len(missing) == 0 {
		return
	}
	sort.Strings(missing)
	fmt.Fprintf(w, "\nADD THIS to %s, or every configuration will build against %q:\n",
		strings.Join(missing, " and "), envs.Default)
	fmt.Fprintln(w, "    <key>PALBASE_ENV</key>")
	fmt.Fprintln(w, "    <string>$(PALBASE_ENV)</string>")
	fmt.Fprintln(w, "  Xcode expands INFOPLIST_KEY_* only into a plist it generates itself; an")
	fmt.Fprintln(w, "  explicit one has to name the key, and then the build configuration decides.")
}

// appInfoPlists finds the Info.plist files that belong to this app, skipping the
// places a dependency's copy lives.
func appInfoPlists(root string) []string {
	var found []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "Pods", ".git", "build", "DerivedData", ".build", "Carthage":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "Info.plist" {
			found = append(found, path)
		}
		return nil
	})
	return found
}
