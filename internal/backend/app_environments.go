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
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/palgroup/palbase-cli/internal/authcontract"
	"github.com/palgroup/palbase-cli/internal/envname"
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
	// fills this in with defaultEnvironment: `main` when there is one, otherwise
	// the first by name that is not `local` — excluded so a build that forgot to
	// say which environment it wanted cannot silently talk to somebody's laptop —
	// and `local` only when it is the only one. It is not a judgement about which
	// one is production.
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
//
// ONE ENVIRONMENT'S DISK DOES NOT DECIDE THE OTHERS' (FR-005). An environment
// that cannot be written stops only itself: every other one is written, and
// the error that comes back is an unwrittenEnvironments naming each one that
// was not. It used to return at the first failure, so one directory nobody
// could create failed every teammate's link.
//
// NO ENVIRONMENT IS LEFT HALF-WRITTEN EITHER. A platform earlier in the list
// can write cleanly for an environment and a LATER one then fail for that same
// environment — the earlier file is not evidence of anything once the
// environment as a whole is unwritten, so it goes back to what was at its
// path before this call, and `written` never names it.
func writeEnvironmentConfigs(platforms []string, envs appEnvironments) ([]string, error) {
	var written []string
	unwritten := unwrittenEnvironments{}
	for _, env := range envs.names() {
		fields := envs.Environments[env]
		var wrote []recordedWrite
		var failure error
		for _, platform := range platforms {
			dest := ConfigPath(env, platform)
			before := snapshotFile(dest)
			if err := writeEnvironmentConfig(dest, fields); err != nil {
				failure = err
				break
			}
			wrote = append(wrote, recordedWrite{path: dest, before: before})
		}
		if failure != nil {
			for _, rec := range wrote {
				_ = rec.before.restore(rec.path)
			}
			unwritten[env] = failure
			continue
		}
		for _, rec := range wrote {
			written = append(written, rec.path)
		}
	}
	if len(unwritten) > 0 {
		return written, unwritten
	}
	return written, nil
}

func writeEnvironmentConfig(dest string, fields appEnvironment) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	blob, err := json.MarshalIndent(mergeConfigWithExisting(dest, fields), "", "  ")
	if err != nil {
		return err
	}
	// 0o600: it carries this environment's publishable key. Not a
	// secret, but not something to widen either.
	return os.WriteFile(dest, append(blob, '\n'), 0o600)
}

// fileSnapshot is what sat at a path immediately before this run touched it —
// its previous bytes and mode, or the fact that nothing was there — so a
// write that turns out to be undone later (FR-005: the environment it belongs
// to becomes unwritten) has something exact to go back to.
type fileSnapshot struct {
	existed bool
	body    []byte
	mode    os.FileMode
}

// snapshotFile reads a path's current state. A path this run has not touched
// yet either has an old file — kept — or none, and "none" is itself the state
// to restore.
func snapshotFile(path string) fileSnapshot {
	info, err := os.Stat(path)
	if err != nil {
		return fileSnapshot{}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return fileSnapshot{}
	}
	return fileSnapshot{existed: true, body: body, mode: info.Mode()}
}

// restore puts path back to exactly what this snapshot recorded.
func (s fileSnapshot) restore(path string) error {
	if !s.existed {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(path, s.body, s.mode)
}

// recordedWrite is one write this run made, kept only long enough to be put
// back if the environment it belongs to turns out not to be written after
// all (FR-005).
type recordedWrite struct {
	path   string
	before fileSnapshot
}

// unwrittenEnvironments is why each environment it names could not be written.
type unwrittenEnvironments map[string]error

func (u unwrittenEnvironments) names() []string {
	out := make([]string, 0, len(u))
	for name := range u {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (u unwrittenEnvironments) Error() string {
	parts := make([]string, 0, len(u))
	for _, name := range u.names() {
		parts = append(parts, fmt.Sprintf("%s: %v", name, u[name]))
	}
	return strings.Join(parts, "; ")
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

// What the line about a directory the sweep must leave behind tells the reader,
// by the build that reads palbase/environments: Xcode compiles every directory
// under it, Gradle and the web generator only the one a build selects.
const (
	xcodeLeftover    = `Xcode compiles everything under palbase/environments, so move it aside if the build reports "Multiple commands produce"`
	selectedLeftover = "a build that selects it still builds an environment this project no longer has, so move it aside"
	// oldSpellingLeftover is selectedLeftover for an old spelling of an
	// environment the project still has — the directory's name, then the one
	// the project gives it now. The project did not lose it; a build that still
	// selects the old spelling reads the wrong files.
	oldSpellingLeftover = "a build that still selects %s reads these files, not %s's, so move it aside"
)

// removeStaleEnvironmentDirs deletes the directory of an environment the project
// no longer has.
//
// Xcode 16 compiles EVERY file under a synchronized folder, so a left-behind
// environment is not clutter — it is a second generated client in the same
// target and a build that stops with "Multiple commands produce …
// PalbaseGenerated.stringsdata". Measured on a real customer app (07.09.2026,
// centauri): `centauri` had outlived a rename to `main` and the build failed
// until that folder was moved aside.
//
// AND GRADLE BUILDS WHAT A BUILD TYPE NAMES (FR-011). An Android build selects
// its environment by name, so a deleted environment's directory is not inert
// there either: measured (verification of 2026-09-25, B4), `featurex/` outlived
// its tenant and the featureX build type went on compiling a dead address, with
// nothing failing.
func removeStaleEnvironmentDirs(root string, keep []string, leftover string, w io.Writer) error {
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
	onDisk := make(map[string]bool, len(entries))
	for _, e := range entries {
		onDisk[e.Name()] = true
	}
	// renamedInto is every listed spelling this sweep made by renaming an old
	// one. It is on disk now, but this run did not write it.
	renamedInto := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() || wanted[e.Name()] {
			continue
		}
		// `local` IS NEVER AN ENVIRONMENT THE PROJECT LOST. It belongs to this
		// MACHINE, and it drops out of the caller's set the moment the stack is
		// down — `palbase stop` is enough. Sweeping on that would delete a
		// directory holding committed products: `isGeneratedEnvironmentFile`
		// counts every platform's files as ours, so an `ios` link in a
		// checkout that is also a web one would take `local/palbe.gen.ts` and
		// `local/web-config.json` with it — while `palbase/client.ts` went on
		// re-exporting the file that had just been deleted.
		//
		// A lingering `local/` is harmless to the build: the selection pattern
		// compiles the chosen environment only.
		//
		// IN EVERY SPELLING A MAC TAKES FOR IT (final review, Minor #7). The
		// exemption compared the name exactly, so `Local/` was swept like an
		// environment the project lost — while on a Mac's disk it IS
		// `local/`, the directory client.ts re-exports this machine's client
		// from and a `local` build type reads. So another spelling is never
		// swept and never respelled as anything but `local`: renamed into it
		// below only when this run wrote local, which on a Mac it did into
		// that very directory.
		atLocal := envname.SameDirectory(e.Name(), localEnvName)
		if atLocal && !wanted[localEnvName] {
			continue
		}
		dir := filepath.Join(base, e.Name())
		inside, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		// AN EMPTY DIRECTORY IS LEFT, AND NOTHING IS SAID ABOUT IT (T014
		// review). The link sweeps its stage, and publishing carries file
		// changes only: the stage lost the directory, the checkout kept it, and
		// every link said "removed" again. An empty directory builds nothing.
		//
		// THAT INCLUDES AN EMPTY OLD SPELLING (T016 review). Renamed in the
		// stage, it held no file for the publish to carry, so the checkout
		// kept the old spelling under a line saying it was renamed — on every
		// link. One this run wrote into is not empty here, and is renamed.
		if len(inside) == 0 {
			continue
		}
		// AN ENVIRONMENT IN ANOTHER LETTER CASE IS NOT A LOST ONE (FR-012).
		// Names were compared exactly, so once the project spelled `Staging`
		// as `staging`, `Staging/` read as gone — and on a Mac it was the very
		// directory this link had just written staging's files into, because
		// the disk takes both spellings for one name. Measured for Apple: the
		// link deleted what it had written. It is renamed instead, whatever it
		// holds: nothing is lost by a rename.
		//
		// NOT WHEN TWO LISTED NAMES SHARE IT (FR-003): both were skipped, and
		// nothing says which spelling it should have. NOR WHEN THE EXACT
		// SPELLING IS ON DISK TOO — only a case-sensitive disk holds both, and
		// there the exact one is what this run wrote; the other is swept.
		//
		// UNLESS THIS SWEEP PUT THE EXACT SPELLING THERE (T016 review). A
		// case-sensitive disk can hold two old spellings, `STAGING/` and
		// `Staging/`. The first was renamed to `staging`, and the second then
		// read as an old spelling beside the one this run wrote, and went —
		// measured: the newer of the two copies was deleted and the older kept.
		// Neither is this run's, and nothing says which one is the
		// environment, so the second is left and said.
		lost := true
		reason := "the project no longer has that environment"
		tail := leftover
		switch spelled := foldedOnly(e.Name(), wanted, nil); {
		case len(spelled) > 1:
			continue
		case len(spelled) == 1 && renamedInto[spelled[0]]:
			fmt.Fprintf(w, "left %s — the project spells that environment %s now, and %s already holds it; "+
				"move one aside\n", shownEnvDir(e.Name()), envname.Label(spelled[0]), shownEnvDir(spelled[0]))
			continue
		case len(spelled) == 1 && !onDisk[spelled[0]]:
			if err := os.Rename(dir, filepath.Join(base, spelled[0])); err != nil {
				return err
			}
			onDisk[e.Name()], onDisk[spelled[0]] = false, true
			renamedInto[spelled[0]] = true
			fmt.Fprintf(w, "renamed %s to %s — the project spells that environment %s now, and a build finds "+
				"its directory by the exact name\n", shownEnvDir(e.Name()), envname.Label(spelled[0]), envname.Label(spelled[0]))
			continue
		case len(spelled) == 1:
			lost = false
			reason = "the project spells that environment " + envname.Label(spelled[0]) + " now"
			if leftover == selectedLeftover {
				tail = fmt.Sprintf(oldSpellingLeftover, envname.Label(e.Name()), envname.Label(spelled[0]))
			}
		}
		// Beside `local/` itself — only a case-sensitive disk holds both —
		// the other spelling is left: it is what a Mac clone takes for local/.
		if atLocal {
			continue
		}
		ours := true
		for _, f := range inside {
			// A FILE BROWSER'S LEFTOVER PROTECTS NOTHING (final review, Minor
			// #4): it goes with the directory.
			if f.Type().IsRegular() && isFileBrowserNoise(f.Name()) {
				continue
			}
			// ONLY A REGULAR FILE CAN BE OURS (T014 review): this CLI writes
			// files here, never a directory or a link. A directory named
			// `openapi.json/` passed on its name, and RemoveAll took the files
			// somebody kept inside it.
			if !f.Type().IsRegular() || !isGeneratedEnvironmentFile(f.Name()) {
				ours = false
				break
			}
		}
		if !ours {
			// A WRITER MUST NOT DELETE WHAT IT CANNOT REPRODUCE. The developer
			// gets the path and the reason; the build error they would otherwise
			// chase is spelled out for them.
			//
			// AND THE REASON IS THE TRUE ONE (T016 review): an old spelling
			// belongs to an environment the project still has.
			if lost {
				fmt.Fprintf(w, "%s belongs to no environment in this project and holds files Palbase "+
					"did not write — %s\n", shownEnvDir(e.Name()), leftover)
			} else {
				fmt.Fprintf(w, "%s holds files Palbase did not write (%s) — %s\n", shownEnvDir(e.Name()), reason, tail)
			}
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		fmt.Fprintf(w, "removed %s (%s)\n", shownEnvDir(e.Name()), reason)
	}
	return nil
}

// shownEnvDir is how a line names one directory under palbase/environments.
//
// RELATIVE TO THE CHECKOUT, because the sweep runs inside the link's stage: an
// absolute path there is the stage's, and swapping the stage back for the
// checkout left `/private/private/var/…` on a Mac (measured — the stage is
// named under /var, the working directory reports /private/var). And a name
// that is not a plain word is quoted: it came from a listing an older CLI did
// not check (FR-006).
func shownEnvDir(name string) string {
	return path.Join(rootDir, envSubdir) + "/" + envname.Label(name)
}

// shownConfigPath is how a line names one platform's config in one
// environment's directory: shownEnvDir, then the file ConfigPath declares.
func shownConfigPath(name, platform string) string {
	return shownEnvDir(name) + "/" + path.Base(ConfigPath(name, platform))
}

// removeThisMachinesOldMain takes away the `main/` an older link wrote for the
// stack on THIS machine (FR-010).
//
// Before that stack was named `local`, a link to it — a `palbase start`
// record, or a loopback address linked by hand — wrote `main/` with 127.0.0.1
// in it, and a release mapped to main shipped an address that exists on one
// laptop. A link now writes that stack to `local/`, so a `main/` whose every
// config points at this machine is the same stack under its old name, and it
// goes. One whose config points anywhere else is somebody's main and stays;
// one that holds a file Palbase never writes is said and left to its owner.
func removeThisMachinesOldMain(root string, w io.Writer) error {
	dir := filepath.Join(root, filepath.FromSlash(EnvDir(soleEnvName)))
	// ONLY A DIRECTORY IS AN OLD main/ (T014 review). A file by that name
	// failed the link with "not a directory", and a symlink would be read — and
	// judged — through wherever it points. Neither is this function's to touch.
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	loopback, foreign := false, false
	for _, e := range entries {
		switch {
		// A file browser's leftover goes with the directory, as in
		// removeStaleEnvironmentDirs.
		case e.Type().IsRegular() && isFileBrowserNoise(e.Name()):
		// Only a regular file can be ours: a directory named like one of our
		// files is somebody's (see removeStaleEnvironmentDirs).
		case !e.Type().IsRegular() || !isGeneratedEnvironmentFile(e.Name()):
			foreign = true
		case strings.HasSuffix(e.Name(), "-config.json"):
			var config appEnvironment
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil || json.Unmarshal(raw, &config) != nil || !isLoopbackAddress(config.BaseURL) {
				return nil
			}
			loopback = true
		}
	}
	switch {
	case !loopback:
		return nil
	case foreign:
		fmt.Fprintf(w, "%s holds the stack on this machine under the name an older link gave it, and files Palbase "+
			"did not write — move them aside, then delete it\n", shownEnvDir(soleEnvName))
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	fmt.Fprintf(w, "removed %s (it held the stack on this machine, which is %s/ now)\n", shownEnvDir(soleEnvName), localEnvName)
	return nil
}

// isFileBrowserNoise reports whether a file by that name is what a desktop
// file browser leaves in any directory a person opens in it: Finder's
// `.DS_Store`, Explorer's `Thumbs.db`.
//
// NOBODY'S DATA, AND NOT OURS EITHER. Counted as somebody's file, it kept a
// stale environment directory for ever — "holds files Palbase did not write —
// move it aside" on every link, for a Mac developer who once looked inside it
// in Finder. So it neither protects a directory nor makes one ours: it goes
// with a directory the sweep removes, and nothing else.
func isFileBrowserNoise(name string) bool {
	return name == ".DS_Store" || name == "Thumbs.db"
}

// isGeneratedEnvironmentFile reports whether this CLI wrote a file by that name
// into an environment directory. Derived from C-1 so a new artifact cannot be
// forgotten here and turn a cleanup into a refusal.
func isGeneratedEnvironmentFile(name string) bool {
	const probe = "probe"
	known := []string{
		path.Base(SpecPath(probe)), path.Base(PlistPath(probe)),
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
// complaint and would otherwise read as "no roles at all".
//
// WHO STILL ASKS THIS, now that the spec round does not: `readStackNames`, for
// the role NAMES `palbase build` renders into a checkout's name cache. That is a
// different question from the one this file used to answer, and it is why this
// function outlived the artifact it was written for.
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
		fmt.Fprintf(w, "\n%s and %s serve different contracts:\n", envname.Label(env), envname.Label(base))
		for _, path := range only {
			fmt.Fprintf(w, "  only in %s:  %s\n", envname.Label(env), path)
		}
		for _, path := range missing {
			fmt.Fprintf(w, "  not in %s:   %s\n", envname.Label(env), path)
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
// writesPerEnvironmentArtifacts answers whether THIS checkout has anything that
// reads them.
//
// MEASURED, NOT ASSUMED: the consumers of `palbase/environments/<env>/` are
// `palbe` (the TypeScript generator, shipped in @palbase/web) and
// `palbase-swiftgen`. Both run in an APP checkout. A backend-only checkout was
// given `openapi.json` on every link and nothing in the
// product ever read them — a diff on every branch, for no reader. The
// codebase already carried the lesson in prose (`web_wiring.go`: "a contract
// nobody reads"); this is the same sentence as a condition.
//
// `palbase spec` still writes them on request: that verb is somebody ASKING.
func writesPerEnvironmentArtifacts(platforms []string) bool {
	return len(platforms) > 0
}

// describeLimit is how many environments one link describes at once. A
// sleeping environment waits out the ready budget, and four of them one after
// another would make a link take minutes for what is a handful of reads.
var describeLimit = 4

// environmentAddress is the address one environment of a project serves on. A
// variable so a test can point refs at its own servers; in the binary it is
// addressOf, whose host is the one this CLI was configured with.
var environmentAddress = addressOf

// describedEnvironment is everything one environment answered, read before
// anything is written.
type describedEnvironment struct {
	entry      appEnvironment
	spec       []byte
	noContract error // the environment answered and has no contract to give, in the project's own words
	err        error // the environment could not be described this run
}

// describeEnvironment reads one environment: whether it serves (waiting out the
// ready budget, which is what wakes a sleeping one), its keys, its contract and,
// when a generator will read them, its roles. It writes nothing.
func describeEnvironment(ctx context.Context, addr string, insecure, readRoles bool) describedEnvironment {
	var d describedEnvironment
	if _, err := describeStack(ctx, addr, insecure); err != nil {
		d.err = err
		return d
	}
	target := Target{URL: addr, Insecure: insecure}
	key, root, err := projectKeys(ctx, target)
	if err != nil {
		d.err = err
		return d
	}
	d.entry = appEnvironment{AppID: projectAppID, BaseURL: addr, APIKey: key, SealedRoot: root}
	cred, _, err := Credential(addr)
	if err != nil {
		d.err = err
		return d
	}
	switch spec, err := fetchStackSpec(ctx, target, cred); {
	case errors.Is(err, ErrNoContractYet):
		d.noContract = err
		return d
	case err != nil:
		d.err = err
		return d
	default:
		d.spec = spec
	}
	return d
}

// gatherEnvironments reads every environment this app can be built against and
// WRITES NOTHING: it returns the entries, the contracts and the role documents,
// and the caller decides what reaches the checkout. That split is what keeps an
// environment that cannot be read this run from being half-written — a new
// contract beside an old config, or a keyless entry merged over a committed
// key.
//
// With no project (a stack somebody runs, or a loopback address) it describes
// the one address it was given. With a project it describes EVERY environment
// of it, at most describeLimit at once: one that is Failed or being deleted is
// not asked; one that cannot be read is left out and named; the default one
// failing fails the link.
//
// `writeArtifacts` separates REPORTING from WRITING, and the two are genuinely
// different jobs: whether a project has a contract yet is worth saying in every
// checkout, while the role documents only matter where a generator reads them.
func gatherEnvironments(ctx context.Context, primary Target, defaultEnv, defaultKey string, project []Environment, writeArtifacts bool, w io.Writer) (appEnvironments, map[string][]byte, error) {
	envs := appEnvironments{
		Default:      defaultEnv,
		Environments: map[string]appEnvironment{},
	}
	specs := map[string][]byte{}

	if len(project) == 0 {
		primaryEnv := appEnvironment{
			AppID:   projectAppID,
			BaseURL: primary.URL,
			APIKey:  defaultKey,
		}
		// Best effort, and deliberately not fatal: a stack that cannot answer about
		// its sealing root is a stack that seals nothing, which is a legal state and
		// not a reason to refuse the link an operator asked for.
		if _, root, err := projectKeys(ctx, primary); err == nil && root != "" {
			primaryEnv.SealedRoot = root
		}
		envs.Environments[defaultEnv] = primaryEnv
		cred, _, err := Credential(primary.URL)
		if err != nil {
			return appEnvironments{}, nil, err
		}
		// A PROJECT WITH NOTHING DEPLOYED IS STILL A PROJECT YOU CAN BIND TO.
		//
		// This refusal closed a loop on itself: `link` refused because the project
		// had no contract, and the project could get no contract because `push`
		// reads the binding only `link` writes. The environment entry is written
		// either way — it carries the address and the key, which is what `push`
		// needs — and the contract is fetched later by `palbase spec`, or by the
		// next `link`, once something answers.
		switch spec, err := fetchStackSpec(ctx, primary, cred); {
		case errors.Is(err, ErrNoContractYet):
			// ONE CURE PER MISSING CONTRACT. A checkout that gets files hears
			// what ends this beside them (missingContractLine, FR-014), so here
			// it hears the fact alone — the project's own sentence when it gave
			// one, the diagnosis nothing else carries. The stack's own step, or
			// "`palbase spec` fills it in", as well was two different next
			// steps for one gap (and for the stack on this machine, a push it
			// refuses).
			if writeArtifacts {
				fmt.Fprintf(w, "%s\n", noContractFact(err))
			} else {
				fmt.Fprintf(w, "%v\n", err)
				fmt.Fprintf(w, "  the link is recorded; `palbase spec` fills the contract in once something answers\n")
			}
		case err != nil:
			return appEnvironments{}, nil, err
		default:
			specs[defaultEnv] = spec
		}
	} else {
		type job struct {
			env  Environment
			addr string
		}
		var jobs []job
		for _, e := range project {
			if unavailableEnvironment(e.Status) {
				fmt.Fprintf(w, "%s is %s — not asked; its files are left as they are\n", envname.Label(e.Name), e.Status)
				continue
			}
			addr, err := environmentAddress(e.Ref)
			if err != nil {
				if e.Name == defaultEnv {
					return appEnvironments{}, nil, err
				}
				fmt.Fprintf(w, "%s could not be read (%v) — its files are left as they are\n", envname.Label(e.Name), err)
				continue
			}
			jobs = append(jobs, job{env: e, addr: addr})
		}
		// THE DEFAULT IS READ, OR THE LINK FAILS (FR-014, FR-082). A default that is
		// Failed, being deleted or missing from the listing is not an environment to
		// leave as it is: it is what a build without a choice talks to.
		defaultAsked := false
		for _, j := range jobs {
			defaultAsked = defaultAsked || j.env.Name == defaultEnv
		}
		if !defaultAsked {
			for _, e := range project {
				if e.Name == defaultEnv {
					return appEnvironments{}, nil, fmt.Errorf("%s is %s, so there is nothing to read from it", envname.Label(e.Name), e.Status)
				}
			}
			return appEnvironments{}, nil, fmt.Errorf("%s is not an environment of this project", envname.Label(defaultEnv))
		}
		// The workers only fill their own slot; the map and the output are built
		// once they are done, in the project's own order.
		results := make([]describedEnvironment, len(jobs))
		slots := make(chan struct{}, describeLimit)
		var wg sync.WaitGroup
		for i, j := range jobs {
			wg.Add(1)
			go func(i int, addr string) {
				defer wg.Done()
				slots <- struct{}{}
				defer func() { <-slots }()
				results[i] = describeEnvironment(ctx, addr, primary.Insecure, writeArtifacts)
			}(i, j.addr)
		}
		wg.Wait()
		for i, j := range jobs {
			d, name := results[i], j.env.Name
			if d.err != nil {
				if name == defaultEnv {
					return appEnvironments{}, nil, fmt.Errorf("%s: %w", envname.Label(name), d.err)
				}
				fmt.Fprintf(w, "%s could not be read (%v) — its files are left as they are; "+
					"run `palbase link` again once it answers\n", envname.Label(name), d.err)
				continue
			}
			envs.Environments[name] = d.entry
			if d.noContract != nil {
				// THE PROJECT'S OWN SENTENCE IS KEPT. "Nothing deployed" and "deployed,
				// but the runtime could not build a document" arrive as the same error,
				// and telling the second one to push again is how a diagnosis took hours.
				// Its sentence, not its step: the stack's `palbase push` names no
				// environment, and this line names the one it means (FR-014).
				fmt.Fprintf(w, "%s has no contract to give (%s) — `palbase push --env %s`\n",
					envname.Label(name), noContractFact(d.noContract), envname.ShellWord(name))
			} else {
				specs[name] = d.spec
			}
		}
	}

	// The stack on this machine, when there is one and it is not already the
	// target. A target this link names `local` IS that stack (FR-010): another
	// one found by group would overwrite the address the person linked.
	if defaultEnv == localEnvName {
		return envs, specs, nil
	}
	localURL, looked := findLocalStack(primary)
	if localURL == "" {
		// A MACHINE THAT RUNS STACKS, NONE OF THEM THIS CHECKOUT'S: said, because
		// the person who started one expects local/ and silence reads as "link
		// forgot it". A machine that runs none is a cloud-only setup — nothing
		// to say.
		if running := registeredStackGroups(); len(running) > 0 {
			fmt.Fprintf(w, "local: no stack on this machine is registered as %s (registered: %s) — local/ is not written; "+
				"`palbase start` registers a stack under the name of the project its checkout is linked to, "+
				"or under its directory's name when that checkout is not linked\n",
				strings.Join(looked, " or "), strings.Join(running, ", "))
		}
		return envs, specs, nil
	}
	if localURL == primary.URL {
		return envs, specs, nil
	}

	// BOTH FILES OR NEITHER (FR-009). A stack that could not give its key used to
	// get a keyless entry ("a missing entry would be a build configuration that
	// vanishes"), and one that gave a key but no contract got a config with no
	// contract beside it. Both are inputs a generator refuses — the Android
	// plugin as "inputs are incomplete", on every teammate's debug build once the
	// half was committed — and the advice to fill it in with `palbase spec` could
	// not: spec writes a contract, never a config. What this run could not read
	// is said, and nothing is written for it.
	skipLocal := func(why string) (appEnvironments, map[string][]byte, error) {
		fmt.Fprintf(w, "local: %s %s — nothing is written for local; `palbase start`, then `palbase link` here\n", localURL, why)
		return envs, specs, nil
	}
	localTarget := Target{URL: localURL, Local: true}
	localCred, _, credErr := Credential(localURL)
	if credErr != nil {
		return skipLocal("is registered but this machine holds no credential for it")
	}
	localKey, keyErr := projectPublishableKey(ctx, localTarget)
	if keyErr != nil {
		return skipLocal(fmt.Sprintf("did not give its key (%v)", keyErr))
	}
	localSpec, specErr := fetchStackSpec(ctx, localTarget, localCred)
	if specErr != nil {
		// The stack's words, not its step: skipLocal names the one cure, and
		// the stack's `palbase spec` would not write local's config (FR-014).
		return skipLocal(fmt.Sprintf("gave its key but not its contract (%s)", noContractFact(specErr)))
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
	specs[localEnvName] = localSpec
	return envs, specs, nil
}

// findLocalStack is the address of the stack `palbase start` registered for this
// target's project, or "" — and every group it looked under, in order.
func findLocalStack(target Target) (url string, looked []string) {
	looked = localStackGroups(target)
	for _, group := range looked {
		if url := LookupLocalStack(group); url != "" {
			return url, looked
		}
	}
	return "", looked
}

// localStackGroups are the groups a target's local stack may be registered
// under, in the order they are tried (FR-008).
//
// THE PROJECT'S NAME FIRST, because it is `start`'s own rule (start.go
// groupName): a backend checkout linked to a project registers its stack under
// that project's name. This used to be the APP checkout's directory name, so an
// app in `MyApp/` never found the stack a backend linked to `todoapp` had
// started — measured on 0.71.2: link wrote main/ and no local/ at all. The
// directory name stays as the second try: it is what `start` registers in a
// checkout that is not linked, and what a monorepo's app and backend share.
func localStackGroups(target Target) []string {
	var groups []string
	add := func(name string) {
		if name == "" {
			return // sanitiseGroup answers "project" for it: a group nobody chose
		}
		if group := sanitiseGroup(name); !slices.Contains(groups, group) {
			groups = append(groups, group)
		}
	}
	// THE NAME, NOT THE IDENTITY. `Target.Project` used to be what a person
	// called their project; it is the product ID now (`prd_9f21c7`), and that
	// value would become the docker compose project name — every local
	// container renamed to an opaque string nobody typed. `start` falls back to
	// the ID only when the record carries no name, and so does this.
	if target.Name != "" {
		add(target.Name)
	} else {
		add(target.Project)
	}
	// The REAL checkout's name, not the working directory's: `link` runs in a
	// stage, whose directory is a temporary name nobody registered.
	root := target.checkoutRoot
	if root == "" {
		root, _ = os.Getwd()
	}
	if root != "" {
		add(filepath.Base(root))
	}
	return groups
}

// missingContractLine is what a link says about an environment it wrote a
// config for and no contract (FR-014) — `earlier` when a contract an earlier
// link wrote is still on disk, which is then what a build reads.
//
// WHAT ENDS IT DEPENDS ON WHERE THE CONTRACT COMES FROM. A project's environment
// serves what was last pushed to it, and --env names it. A stack somebody hosts,
// linked by address, has no project to name one in. The stack `palbase start`
// runs serves the directory it mounted, and `palbase push` refuses to publish to
// it (stack_push.go) — for `local` the cure is a start. Each ends with a link:
// that is what brings the contract into this checkout.
//
// THE NAME INSIDE THE COMMAND IS A SHELL WORD, not a label: it is text a person
// pastes, and Label's %q is not shell quoting (T007/T008).
func missingContractLine(name string, project, earlier bool) string {
	cure := contractCure(name, project)
	spec := shownEnvDir(name) + "/openapi.json"
	if earlier {
		return fmt.Sprintf("%s gave no contract this time, so %s is the one an earlier link wrote — %s\n",
			envname.Label(name), spec, cure)
	}
	return fmt.Sprintf("%s has no contract yet, so %s is not written and no client is generated for it — %s\n",
		envname.Label(name), spec, cure)
}

// contractCure is what brings name's contract into this checkout — the cure
// missingContractLine and doctor's Android section both name.
//
// The name inside the command is a shell word, not a label (see
// missingContractLine): it is text a person pastes.
func contractCure(name string, project bool) string {
	switch {
	case name == localEnvName:
		return "`palbase start` in the backend, then `palbase link` here"
	case project:
		return fmt.Sprintf("`palbase push --env %s`, then `palbase link` here", envname.ShellWord(name))
	default:
		return "`palbase push`, then `palbase link` here"
	}
}

func writeSpec(env string, spec []byte) error {
	path := specPath(env)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, spec, 0o644)
}

// generateForEnvironments emits one client per environment, and one plist for
// all of them — `palbase spec` and `push`'s path, which knows the environments
// only from the disk. `link` knows the project's listing and sweeps with it
// before it generates (runLinkPrepared).
func generateForEnvironments(ctx context.Context, envs appEnvironments, w io.Writer) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	// A LEFT-BEHIND ENVIRONMENT BREAKS THE BUILD, so it goes first.
	if err := removeStaleEnvironmentDirs(root, envs.names(), xcodeLeftover, w); err != nil {
		return err
	}
	return generateForEnvironmentsAt(ctx, envs, w, "")
}

// generateForEnvironmentsAt emits each environment's Swift client and plist
// into the working directory — the checkout for `palbase spec` and `push`, the
// stage for `link` — from the contract and Apple configs already on disk there.
// An environment with no contract yet is passed over.
//
// IT SWEEPS NOTHING. Each caller sweeps first with the list it knows:
// generateForEnvironments with the directories on disk, `link` with the
// project's listing (runLinkPrepared).
//
// toolRoot is the project the SDK's generator is found from, "" meaning the
// working directory. `link` passes the checkout: a local package's relative
// path, Package.resolved and the project's SDK requirement all resolve from
// there, not from the stage.
func generateForEnvironmentsAt(ctx context.Context, envs appEnvironments, w io.Writer, toolRoot string) error {
	root, err := os.Getwd()
	if err != nil {
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
		// THE NAME IS SHOWN, NEVER PRINTED RAW (FR-006). The directory is on
		// disk as an older CLI made it from a listing, and three verbs print
		// this error — `status`, `spec`, the web wiring. The system's error
		// carries the raw path, so only its reason is kept.
		if readErr != nil {
			var pathErr *fs.PathError
			if errors.As(readErr, &pathErr) {
				readErr = pathErr.Err
			}
			return appEnvironments{}, fmt.Errorf("read %s: %w", shownConfigPath(e.Name(), platform), readErr)
		}
		var env appEnvironment
		if err := json.Unmarshal(raw, &env); err != nil {
			return appEnvironments{}, fmt.Errorf("read %s: %w", shownConfigPath(e.Name(), platform), err)
		}
		out.Environments[e.Name()] = env
	}
	out.Default = diskDefault(out.names())
	return out, nil
}

// diskDefault is the environment a checkout carrying these names acts on when
// nothing chose one.
//
// `local` is not the default while any other environment is here: a build
// that forgot to say which environment it wanted must not silently talk to a
// developer's laptop. A checkout that carries only `local` has nothing else the
// default could be.
func diskDefault(names []string) string {
	if d := defaultEnvironment(names); d != "" {
		return d
	}
	if len(names) == 0 {
		return ""
	}
	return slices.Min(names)
}

// unavailableEnvironment is D-7's filter: an environment in these phases
// answers nothing, and waiting out the ready budget on it is time spent to
// learn what the listing already said.
func unavailableEnvironment(status string) bool {
	return strings.EqualFold(status, "Failed") || strings.EqualFold(status, "Deleting")
}

// defaultEnvironment is the ONE rule for "which environment does a checkout act
// on by default" — `main` when it exists, otherwise the first by name, never
// `local`. It used to live twice: link preferred `main`, the disk reader took
// the first non-local name, and the two agreed only while a checkout carried a
// single environment. One link now writes every environment, so they would
// have disagreed on the next link.
func defaultEnvironment(names []string) string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	first := ""
	for _, n := range sorted {
		if n == localEnvName {
			continue
		}
		if strings.EqualFold(n, soleEnvName) {
			return n
		}
		if first == "" {
			first = n
		}
	}
	return first
}
