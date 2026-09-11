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
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/palgroup/palbase-cli/internal/authcontract"
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
	SealedRoot    string                       `json:"sealed_root,omitempty"`
	OAuth         *authcontract.SocialSnapshot `json:"oauth,omitempty"`
	Notifications json.RawMessage              `json:"notifications,omitempty"`
	Integrity     json.RawMessage              `json:"integrity,omitempty"`
}

// appEnvironments is what the CLI writes and the generator reads.
type appEnvironments struct {
	// Default is DERIVED, never read from a file, and it is not "production".
	//
	// Nothing writes `default_environment` any longer: each environment's config
	// holds that environment's own fields, so the name is the DIRECTORY and a
	// key inside the file would be a second copy of it. `readAppEnvironments`
	// fills this in by taking the first non-`local` environment IN NAME ORDER —
	// `local` is excluded so a build that forgot to say which environment it
	// wanted cannot silently talk to somebody's laptop, and the rest is
	// alphabetical, not a judgement about which one is production.
	Default      string                    `json:"default_environment"`
	Environments map[string]appEnvironment `json:"environments"`
}

// localEnvName is the environment every app checkout gets for free: the stack
// running on this machine.
const localEnvName = "local"

func (a appEnvironments) names() []string {
	out := make([]string, 0, len(a.Environments))
	for name := range a.Environments {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// writeEnvironmentConfigs writes ONE config per environment per platform, flat,
// into that environment's own directory.
//
// There used to be two writers with two shapes: a native slot that was a MAP of
// every environment (`{default_environment, environments:{…}}`) and a FLAT web
// config, because "one app binary is built against several environments, a
// deployed web app is one". The map existed only because the file lived at ONE
// path — pointing an app at another environment meant OVERWRITING it. Every
// environment now has its own directory, so the map has nothing left to do and
// both platforms write the same flat shape.
func writeEnvironmentConfigs(platforms []string, envs appEnvironments) ([]string, error) {
	var written []string
	for _, env := range envs.names() {
		fields := envs.Environments[env]
		for _, platform := range platforms {
			dest := ConfigPath(env, platform)
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return nil, err
			}
			blob, err := json.MarshalIndent(mergeConfigWithExisting(dest, fields), "", "  ")
			if err != nil {
				return nil, err
			}
			// 0o600: it carries this environment\'s publishable key. Not a
			// secret, but not something to widen either.
			if err := os.WriteFile(dest, append(blob, '\n'), 0o600); err != nil {
				return nil, err
			}
			written = append(written, dest)
		}
	}
	return written, nil
}

// mergeConfigWithExisting keeps app metadata this link did not supply.
//
// Only for the SAME backend: a value carried over from another project would
// name a channel nobody else uses (measured 2026-08-25). OAuth is ALWAYS
// supplied by the current link and never merged — a stale snapshot is a client
// that signs in against the wrong project.
//
// `environment_ref` is not preserved and never was after it was removed: the
// identity comes from the key and nowhere else, and a copy that must equal its
// original is not a second fact but a second chance to be wrong.
func mergeConfigWithExisting(dest string, next appEnvironment) appEnvironment {
	raw, err := os.ReadFile(dest)
	if err != nil {
		return next
	}
	var prev appEnvironment
	if err := json.Unmarshal(raw, &prev); err != nil {
		return next
	}
	if prev.BaseURL != next.BaseURL {
		return next
	}
	if len(next.Notifications) == 0 {
		next.Notifications = prev.Notifications
	}
	if len(next.Integrity) == 0 {
		next.Integrity = prev.Integrity
	}
	return next
}

// removeStaleEnvironmentDirs deletes the directory of an environment the project
// no longer has.
//
// Xcode 16 compiles EVERY file under a synchronized folder, so a left-behind
// environment is not clutter — it is a second generated client in the same
// target and a build that stops with "Multiple commands produce …
// PalbaseGenerated.stringsdata". Measured on a real customer app (07.09.2026,
// centauri): `centauri` had outlived a rename to `main` and the build failed
// until that folder was moved aside.
func removeStaleEnvironmentDirs(root string, keep []string, w io.Writer) error {
	wanted := make(map[string]bool, len(keep))
	for _, env := range keep {
		wanted[env] = true
	}
	// From the declaration, never spelled again: this must be exactly the
	// directory `EnvDir` creates its environments in.
	base := filepath.Join(root, filepath.Dir(filepath.FromSlash(EnvDir("any"))))
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || wanted[e.Name()] {
			continue
		}
		// `local` IS NEVER AN ENVIRONMENT THE PROJECT LOST. It belongs to this
		// MACHINE, and it drops out of the caller's set the moment the stack is
		// down — `palbase stop` is enough. Sweeping on that would delete a
		// directory holding committed products: this runs on the Apple branch
		// only, and `isGeneratedEnvironmentFile` counts the WEB client and its
		// config as ours, so an `ios` link in a checkout that is also a web one
		// would take `local/palbe.gen.ts` and `local/web-config.json` with it —
		// while `palbase/client.ts` went on re-exporting the file that had just
		// been deleted.
		//
		// A lingering `local/` is harmless to the build: the selection pattern
		// compiles the chosen environment only.
		if e.Name() == localEnvName {
			continue
		}
		dir := filepath.Join(base, e.Name())
		inside, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		ours := true
		for _, f := range inside {
			if !isGeneratedEnvironmentFile(f.Name()) {
				ours = false
				break
			}
		}
		if !ours {
			// A WRITER MUST NOT DELETE WHAT IT CANNOT REPRODUCE. The developer
			// gets the path and the reason; the build error they would otherwise
			// chase is spelled out for them.
			fmt.Fprintf(w, "%s belongs to no environment in this project and holds files Palbase "+
				"did not write — Xcode compiles everything under palbase/environments, so move it "+
				"aside if the build reports \"Multiple commands produce\"\n", dir)
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		fmt.Fprintf(w, "removed %s (the project no longer has that environment)\n", dir)
	}
	return nil
}

// isGeneratedEnvironmentFile reports whether this CLI wrote a file by that name
// into an environment directory. Derived from C-1 so a new artifact cannot be
// forgotten here and turn a cleanup into a refusal.
func isGeneratedEnvironmentFile(name string) bool {
	const probe = "probe"
	known := []string{
		path.Base(SpecPath(probe)), path.Base(RolesPath(probe)), path.Base(PlistPath(probe)),
		path.Base(GeneratedPath(probe, "ios")), path.Base(GeneratedPath(probe, webPlatform)),
	}
	for _, platform := range []string{"ios", "macos", "android", webPlatform} {
		known = append(known, path.Base(ConfigPath(probe, platform)))
	}
	for _, k := range known {
		if k != "" && k == name {
			return true
		}
	}
	// A CUSTOM `--out` NAME IS STILL OURS. `palbase link --platform web --out
	// api.gen.ts` puts a generated client here under a name this list cannot
	// know, and the sweep then leaves the whole directory behind as "holds files
	// Palbase did not write" — so two environments' clients sit under
	// `palbase/environments`, which is exactly the "Multiple commands produce"
	// build failure the sweep exists to prevent. It is the same loose definition
	// as C-2, pulling the other way.
	//
	// The generator only ever writes TypeScript here, so a `.ts` file inside an
	// environment directory is ours. Anything else — a note, an asset somebody
	// dropped in — still protects the directory.
	return strings.HasSuffix(name, ".ts")
}

// specPath is where one environment's contract is committed — C-1 owns the
// shape; this name stays so the call sites read as they always did.
func specPath(env string) string { return SpecPath(env) }

// rolesPath is where one environment's ROLE DEFINITIONS are committed: beside
// its contract, in the same directory, differing only in name.
//
// Beside it rather than in a directory of its own because the two documents
// describe the same environment at the same moment and are fetched by one act —
// and because a generator handed the spec can then find the roles BY RULE
// instead of by a second setting somebody has to keep in step. `palbase-swiftgen`
// and `palbe-gen` live in other packages and cannot call this function; the rule
// is the only thing they can share.
//
// THERE IS NO SECOND COPY ANY MORE. The web SDK used to read its own
// `Palbase/roles.json`, mirrored here by `copyRolesToWeb` — two committed files
// with the same bytes, and a generator that could be handed a stale one. Both
// generators now read this one.
func rolesPath(env string) string { return RolesPath(env) }

// stackRole is one role definition as the generators need it.
//
// WHAT IS NOT HERE IS THE POINT. `GET /admin/roles` also answers `userCount`,
// and this artifact is COMMITTED: carrying a counter would rewrite the file
// every time somebody signed up, produce a diff in every review, and none of it
// would change a single generated type. A role's NAME, what it may do, and
// whether new users get it are the definition; the rest is runtime state and
// belongs to `palbase roles list`.
type stackRole struct {
	Name string `json:"name"`
	// omitempty: a role whose name says everything needs no description, and
	// writing "" would look like an empty one somebody wrote on purpose.
	Description string   `json:"description,omitempty"`
	IsDefault   bool     `json:"isDefault"`
	Permissions []string `json:"permissions"`
}

// stackRoles is the artifact, and the shape both generators read.
type stackRoles struct {
	Roles []stackRole `json:"roles"`
}

// fetchStackRoles asks the stack which roles it defines and what each one grants.
//
// A 404 IS AN ANSWER, and treating it as one is the whole tolerance this needs.
// A stack older than the roles surface has no such door, "this project defines
// no roles" is the truth for it, and that comes back as an empty list with no
// error — so the spec round it is part of succeeds. Refusing here would break
// `palbase spec` in every project that has not adopted RBAC, which today is all
// of them.
//
// Every OTHER refusal is "could not tell", and it returns an error. So does a
// 200 with no `roles` key: a proxy's JSON page decodes into this struct without
// complaint and would otherwise read as "no roles at all". The difference has to
// survive to the write — see refreshRoles.
func fetchStackRoles(ctx context.Context, target Target, cred Credentials) (stackRoles, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(target.URL, "/")+"/admin/roles", nil)
	if err != nil {
		return stackRoles{}, err
	}
	cred.Apply(req)
	req.Header.Set("Accept", "application/json")

	res, err := stackClient(target).Do(req)
	if err != nil {
		return stackRoles{}, fmt.Errorf("reach %s: %w", target.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := readCapped(res.Body, 4<<20, req.URL.String())
	if err != nil {
		return stackRoles{}, err
	}

	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return stackRoles{Roles: []stackRole{}}, nil
	default:
		return stackRoles{}, fmt.Errorf("the stack's roles came back %d: %s",
			res.StatusCode, trimBody(body))
	}

	var doc struct {
		Roles *[]stackRole `json:"roles"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return stackRoles{}, fmt.Errorf("the stack's roles did not parse: %w", err)
	}
	if doc.Roles == nil {
		return stackRoles{}, errors.New("the stack answered about roles without a roles list")
	}
	return normalizeRoles(stackRoles{Roles: *doc.Roles}), nil
}

// normalizeRoles makes the artifact a function of the DEFINITIONS and nothing
// else.
//
// It is committed, so two runs against an unchanged stack have to produce
// identical bytes — a diff that depends on the order rows came back is a diff
// nobody can read. The endpoint happens to order both today; ordering it here
// means the committed file does not depend on that staying true.
//
// A nil list becomes an empty one: `null` reads as "unknown", `[]` reads as
// "none", and only one of those is what a stack with no roles said.
func normalizeRoles(in stackRoles) stackRoles {
	out := stackRoles{Roles: make([]stackRole, 0, len(in.Roles))}
	for _, role := range in.Roles {
		perms := make([]string, len(role.Permissions))
		copy(perms, role.Permissions)
		sort.Strings(perms)
		role.Permissions = perms
		out.Roles = append(out.Roles, role)
	}
	sort.Slice(out.Roles, func(i, j int) bool { return out.Roles[i].Name < out.Roles[j].Name })
	return out
}

// refreshRoles brings one environment's role definitions down beside its
// contract.
//
// BEST EFFORT, AND NEVER FATAL ON THE FETCH. Fetching the contract is what a
// spec round is for; the roles beside it are an addendum, and a stack that will
// not answer about them is not a reason to refuse the refresh somebody asked
// for.
//
// But "COULD NOT TELL" IS NOT "THERE ARE NONE", and that difference is why this
// is not one line. On a 404 the stack has ANSWERED — it defines no roles — and
// the empty artifact is the truth. On anything else the file on disk is left
// exactly as it is: writing an empty list would delete definitions this run
// could not produce, the generators would emit no constants from it, and the app
// would compile with every permission check it used to make silently gone. The
// reason is printed either way, because an artifact that quietly stopped
// tracking its stack is the only failure mode here nobody would notice.
func refreshRoles(ctx context.Context, target Target, cred Credentials, env string, w io.Writer) error {
	roles, err := fetchStackRoles(ctx, target, cred)
	if err != nil {
		fmt.Fprintf(w, "roles: %s did not answer for %s — %v\n", target.URL, env, err)
		fmt.Fprintf(w, "roles: %s keeps what it last held; the generated role types are NOT refreshed\n",
			rolesPath(env))
		return nil
	}
	if err := writeRolesArtifact(rolesPath(env), roles); err != nil {
		return err
	}
	fmt.Fprintf(w, "✓ wrote %s (%d roles)\n", rolesPath(env), len(roles.Roles))
	return nil
}

func writeRolesArtifact(path string, roles stackRoles) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	blob, err := json.MarshalIndent(roles, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(blob, '\n'), 0o644)
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
	// A PROJECT WITH NOTHING DEPLOYED IS STILL A PROJECT YOU CAN BIND TO.
	//
	// This refusal closed a loop on itself: `link` refused because the project
	// had no contract, and the project could get no contract because `push`
	// reads the binding only `link` writes. The person was told to push by a
	// command that had just made pushing impossible.
	//
	// The environment entry is written either way — it carries the address and
	// the key, which is what `push` needs — and the contract is fetched later by
	// `palbase spec`, or by the next `link`, once something answers. The same
	// leniency the local stack has had all along (see below): a stack that
	// cannot answer is a legal state, not a reason to refuse the link.
	switch spec, err := fetchStackSpec(ctx, target, cred); {
	case errors.Is(err, ErrNoContractYet):
		fmt.Fprintf(w, "%v\n", err)
		fmt.Fprintf(w, "  the link is recorded; `palbase spec` fills the contract in once something answers\n")
	case err != nil:
		return appEnvironments{}, nil, err
	default:
		if err := writeSpec(primary, spec); err != nil {
			return appEnvironments{}, nil, err
		}
		if err := refreshRoles(ctx, target, cred, primary, w); err != nil {
			return appEnvironments{}, nil, err
		}
		specs[primary] = spec
	}

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
		if err := refreshRoles(ctx, localTarget, localCred, localEnvName, w); err != nil {
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
	if target.checkoutRoot != "" {
		return filepath.Base(target.checkoutRoot)
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
	return generateForEnvironmentsAt(ctx, envs, w, "")
}

func generateForEnvironmentsAt(ctx context.Context, envs appEnvironments, w io.Writer, toolRoot string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	// A LEFT-BEHIND ENVIRONMENT BREAKS THE BUILD, so it goes first.
	if err := removeStaleEnvironmentDirs(root, envs.names(), w); err != nil {
		return err
	}

	if toolRoot == "" {
		toolRoot = root
	}
	tool, err := ensureSwiftgenTool(toolRoot, w)
	if err != nil {
		// The staged link cannot publish a spec without its matching client.
		var stale []string
		for _, env := range envs.names() {
			stale = append(stale, filepath.Join(root, filepath.FromSlash(GeneratedPath(env, "ios"))))
			stale = append(stale, filepath.Join(root, filepath.FromSlash(PlistPath(env))))
		}
		return discardStaleGenerated(err, w, stale...)
	}

	// ONE CLIENT AND ONE PLIST PER ENVIRONMENT, both inside that environment's
	// own directory. The plist used to be written ONCE for all of them, because
	// it carried a map the app indexed by name at runtime; it is now one file
	// per environment and the BUILD picks which one enters the bundle.
	for _, env := range envs.names() {
		spec := specPath(env)
		if !isRegularFile(spec) {
			// An environment whose contract has not been fetched — the local
			// stack while it is down. Inventing an empty client would compile
			// and then 404.
			continue
		}
		out := filepath.Join(root, filepath.FromSlash(GeneratedPath(env, "ios")))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, tool, "--openapi", spec, "--out-swift", out)
		cmd.Stderr = w
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("palbase-swiftgen (%s): %w", env, err)
		}
		fmt.Fprintf(w, "✓ wrote %s\n", out)

		var configFlags []string
		for _, platform := range []string{"ios", "macos"} {
			cfg := ConfigPath(env, platform)
			if isRegularFile(cfg) {
				configFlags = append(configFlags, "--"+platform+"-config", cfg)
			}
		}
		if len(configFlags) == 0 {
			continue // no Apple slot in this checkout
		}
		plist := filepath.Join(root, filepath.FromSlash(PlistPath(env)))
		cmd = exec.CommandContext(ctx, tool, append([]string{"--out-plist", plist}, configFlags...)...)
		cmd.Stderr = w
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("palbase-swiftgen (plist, %s): %w", env, err)
		}
		fmt.Fprintf(w, "✓ wrote %s\n", plist)
	}
	return nil
}

// readAppEnvironments reads back what the link wrote for one platform.
//
// It walks the environment directories and reassembles the map callers still
// think in — the map is gone from DISK, not from the CLI's own vocabulary:
// `status` and the drift report legitimately ask "which environments does this
// checkout carry", and that question is now answered by what is on disk rather
// than by a field somebody could have edited.
func readAppEnvironments(platform string) (appEnvironments, error) {
	// Same declaration as the writer's: a walk that spells the root itself
	// would keep reading the old one after a rename.
	base := filepath.Dir(filepath.FromSlash(EnvDir("any")))
	entries, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return appEnvironments{}, nil
	}
	if err != nil {
		return appEnvironments{}, err
	}
	out := appEnvironments{Environments: map[string]appEnvironment{}}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, readErr := os.ReadFile(ConfigPath(e.Name(), platform))
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return appEnvironments{}, readErr
		}
		var env appEnvironment
		if err := json.Unmarshal(raw, &env); err != nil {
			return appEnvironments{}, fmt.Errorf("read %s: %w", ConfigPath(e.Name(), platform), err)
		}
		out.Environments[e.Name()] = env
	}
	// `local` is never the default: a build that forgot to say which environment
	// it wanted must not silently talk to a developer's laptop.
	for _, name := range out.names() {
		if name != localEnvName {
			out.Default = name
			break
		}
	}
	if out.Default == "" && len(out.Environments) > 0 {
		out.Default = out.names()[0]
	}
	return out, nil
}
