package backend

// status_project.go — `palbase status` for a project you are linked to.
//
// The question it answers is "is what I am holding the same as what is running",
// and it is worth its own command because every wrong answer looks like
// something else. A stale publishable key looks like a broken login. A client
// generated two deploys ago looks like a backend bug. A local stack that was
// stopped this morning looks like a network problem.
//
// So it reports what it can actually check: what the project is serving, which
// @palbase/backend it runs (the same document `push` reads, so the two verbs
// never disagree), whether the key this app ships still matches the project's,
// and whether the local stack this checkout was pointed at is still up.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/envname"
)

type deploymentState struct {
	Digest        string    `json:"digest"`
	EndpointCount *int      `json:"endpoint_count"`
	ActivatedAt   time.Time `json:"activated_at"`
	SDKVersion    string    `json:"sdk_version"`
}

// statusJSON is `palbase status --json`: what this command can actually check,
// in the shape a script can read.
//
// It names the ADDRESS rather than a project and environment id pair. Those ids
// were the cloud arm's vocabulary, and that arm is gone; what a status answer
// has to make unambiguous is WHICH RUNTIME was looked at (UAT CLI-005), and the
// address is exactly that — for a project on this machine as much as for one in
// the cloud.
type statusJSON struct {
	Project    string           `json:"project"`
	Address    string           `json:"address"`
	Credential statusCredential `json:"credential"`
	// Deployed is nil when nothing has been pushed, or when this stack serves the
	// directory and never follows the deploy pointer. Reason says which.
	Deployed *deploymentState `json:"deployed"`
	Reason   string           `json:"reason,omitempty"`
	// AppKey is "current", "stale" or "unchecked" — the drift the text output
	// warns about, in one word a script can branch on.
	AppKey string `json:"app_key,omitempty"`
	// SDK is the @palbase/backend this project RUNS, off the same document push
	// reads. Deployed.SDKVersion, right above, is what the live ARTIFACT was
	// BUILT with — a different fact, and the reason both are named rather than
	// merged. Empty when the project would not say.
	SDK string `json:"sdk,omitempty"`
}

type statusCredential struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
}

// statusOfProject reports on the project this verb acts on: the one this
// checkout is linked to, or the one the caller selected.
func statusOfProject(cmd *cobra.Command, jsonOut bool) error {
	ctx := cmd.Context()
	resolved, err := Resolve(ctx)
	target := resolved.Acting()
	if err != nil {
		return err
	}
	cred, source, err := Credential(target.URL)
	if err != nil {
		return err
	}
	// THE KEY TO COMPARE IS THE RESOLVED ENVIRONMENT'S. A cloud checkout always
	// resolves one, and a stack running here is `local` on disk. A self-hosted
	// stack, a loopback link and a record from before projects resolve none;
	// then the disk's default is compared.
	keyEnv := resolved.Env
	if keyEnv == "" && target.Local {
		keyEnv = localEnvName
	}
	if jsonOut {
		return statusAsJSON(ctx, cmd, target, keyEnv, cred, string(source))
	}
	// THE RESOLVER NAMES THE ENVIRONMENT; a target cannot (FR-085). This line
	// printed the project alone while the key below was staging's.
	fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s\n", resolved.Describe())

	out := cmd.OutOrStdout()

	fmt.Fprintf(out, "project:      %s\n", target.Describe())
	fmt.Fprintf(out, "address:      %s\n", target.URL)
	fmt.Fprintf(out, "credential:   %s (%s)\n", source, credentialKindWord(cred.Kind))

	// deployments/current, not `deployment`. The singular does not exist, and a
	// 404 from a route that is not there was being read as "you have not deployed
	// yet" — so `palbase status` said "nothing yet" about a project that had 37
	// endpoints live, in the same second `palbase deploys` listed them.
	status, body, err := managementCall(ctx, target, cred, http.MethodGet,
		"/v1/management/deployments/current", nil, "")
	builtWith := "" // the live artifact's SDK, compared with the runtime's below
	switch {
	case err != nil:
		return err
	case status == http.StatusNotFound && target.Local:
		// A stack started HERE serves this directory and never follows the deploy
		// pointer, so "no artifact" is the permanent, correct answer rather than a
		// state to get out of. The generic line below sent people to `palbase
		// push`, which refuses on exactly this target — status was advising a
		// command its own CLI rejects a second later.
		fmt.Fprintln(out, "deployed:     n/a — this stack serves this directory, and rebuilds when you save")
	case status == http.StatusNotFound:
		fmt.Fprintln(out, "deployed:     nothing yet — `palbase push`")
	case status != http.StatusOK:
		return fmt.Errorf("%s answered %d: %s", target.Describe(), status, trimBody(body))
	default:
		var deployed deploymentState
		if err := json.Unmarshal(body, &deployed); err != nil {
			return fmt.Errorf("read the deployment: %w", err)
		}
		digest := deployed.Digest
		if len(digest) > 12 {
			digest = digest[:12]
		}
		fmt.Fprintf(out, "deployed:     %s", digest)
		if deployed.EndpointCount != nil {
			fmt.Fprintf(out, ", %d endpoint(s)", *deployed.EndpointCount)
		}
		// "built with", not "SDK". This number is read off the ACTIVE ARTIFACT's
		// manifest — the SDK that artifact was compiled against — and the bare
		// label made it look like the answer to "which SDK is this project on",
		// which is a different fact from a different document (see the sdk: line
		// below). Two numbers under one word is how somebody reads a version
		// that predicts nothing about what their next push will do.
		if deployed.SDKVersion != "" {
			fmt.Fprintf(out, ", built with SDK %s", deployed.SDKVersion)
			builtWith = deployed.SDKVersion
		}
		fmt.Fprintf(out, "\n              activated %s\n", deployed.ActivatedAt.Local().Format("2006-01-02 15:04"))
	}

	// WHICH SDK THIS PROJECT IS ON — from the document `push` itself reads.
	//
	// projectSDKVersion is the function `push` calls to decide whether the image
	// is about to move (the checkout's SDK is what ships, and the plane brings
	// the image to it), and it asks
	// /.well-known/palbase.json, which the stack answers from a LIVE probe of the
	// runtime (v2 internal/server/wellknown.go). status used to report the
	// artifact's number instead, and the two diverge the moment a project is
	// moved onto a newer runtime without a redeploy — so status handed people a
	// version that predicted nothing about the push they were about to run.
	//
	// Bounded, because this is `status`: describeStack waits up to
	// stackReadyWait (3 minutes) for a project that answers 503, which is right
	// for a push and absurd for a question about the current state.
	//
	// Silent when it cannot be read. Unlike the app key — where saying nothing
	// would read as "your key is fine" — the deployed line above still names the
	// artifact's SDK, so a reader is not left believing something false.
	//
	// NAMED AS THE RUNTIME (palbase-cli#7 §3). The label used to be `sdk:` and the
	// gloss "what this project RUNS", which read as the checkout's own dependency;
	// under a `built with` line carrying another number it looked like the tree
	// had drifted from live, and that is the diagnosis it produced. The number is
	// the stack's, and when the artifact it serves was built with a different SDK
	// the two were moved separately — which is said, not left to be inferred.
	sdkCtx, cancelSDK := context.WithTimeout(ctx, 10*time.Second)
	if running, err := projectSDKVersion(sdkCtx, target, cred); err == nil && running != "" {
		fmt.Fprintf(out, "runtime:      %s %s — the stack serving this project, not this checkout's dependency\n",
			backendPkg, running)
		if builtWith != "" && builtWith != running {
			fmt.Fprintf(out, "              the live artifact was built with %s: the two differ because the runtime\n"+
				"              moved without a redeploy — the next push builds with this checkout's SDK\n", builtWith)
		}
	}
	cancelSDK()

	reportKeyDrift(ctx, target, keyEnv, cred, out)
	reportCommittedDrift(out)

	// The Info.plist warning that used to be repeated here is GONE with the
	// mechanism it belonged to: nothing in the app's build system is ours to
	// require any more, so there is nothing to remind anybody of.
	return nil
}

// reportCommittedDrift says which endpoints the environments this app holds do
// not agree on, from the contracts already on disk.
//
// It asks no environment anything: the contracts were fetched when they were
// linked or refreshed, and reaching production from a laptop to answer a status
// question is a request nobody asked for. What it costs is honesty about
// freshness — this reports the difference between what was FETCHED, which is
// also what the app was built against.
func reportCommittedDrift(out io.Writer) {
	// One contract per environment directory — the shape the layout writes.
	entries, err := os.ReadDir(filepath.Dir(filepath.FromSlash(EnvDir("any"))))
	if err != nil {
		return
	}
	specs := map[string][]byte{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		body, err := os.ReadFile(SpecPath(e.Name()))
		if err != nil {
			continue
		}
		specs[e.Name()] = body
	}
	reportContractDrift(specs, out)
}

func credentialKindWord(kind Kind) string {
	if kind == KindKey {
		return "this project's key"
	}
	return "a person"
}

// reportKeyDrift compares the key this app SHIPS with the one the project hands
// out now.
//
// The drift is ordinary and its symptom is not: a rotated publishable key leaves
// every installed build authenticating with something the project no longer
// accepts, and the app reports it as a sign-in failure. Nothing else in this CLI
// would notice, because the committed slot is a file and files do not expire.
//
// EVERY PLATFORM THIS CHECKOUT SHIPS, EACH NAMED (FR-018). It read the iOS
// configs alone, so an Android or web checkout got no line at all — and a
// checkout with both got "current" for iOS while its Android key was stale.
func reportKeyDrift(ctx context.Context, target Target, env string, cred Credentials, out io.Writer) {
	d := measureAppKeys(ctx, target, env)
	if len(d.stale) > 0 {
		// The keys themselves are not printed — a publishable key is not a secret,
		// but printing two nearly-identical strings invites reading them for the
		// difference instead of running the command that fixes it.
		fmt.Fprintf(out, "app key:      STALE (%s) — %s ships a key this project no longer hands out.\n",
			strings.Join(d.stale, ", "), envname.Label(d.env))
		fmt.Fprintln(out, "              Run `palbase link` to refresh it, then rebuild the app.")
	}
	if d.err != nil {
		// Cannot check is not the same as checked. Silence here would read as
		// "the key is fine".
		fmt.Fprintf(out, "app key:      could not be checked (%v)\n", d.err)
	}
	for _, u := range d.unreadable {
		// A config that cannot be read is not a config that is fine either: the
		// platform is named with the read error, which names the file to fix.
		fmt.Fprintf(out, "app key:      unchecked (%s) — a config here could not be read (%v)\n",
			strings.Join(u.platforms, ", "), u.err)
	}
	if len(d.missing) > 0 {
		// Silence would read as "the key is fine" here too: other environments
		// are on disk, the one this command acts on is not.
		fmt.Fprintf(out, "app key:      unchecked (%s) — %s has no committed config here; `palbase link` writes it\n",
			strings.Join(d.missing, ", "), envname.Label(d.env))
	}
	if len(d.keyless) > 0 {
		// And a config that carries no key — what an older CLI wrote for a
		// stack that was down — ships nothing to compare.
		fmt.Fprintf(out, "app key:      unchecked (%s) — %s ships no key here; `palbase link` writes it\n",
			strings.Join(d.keyless, ", "), envname.Label(d.env))
	}
	if len(d.current) > 0 {
		fmt.Fprintf(out, "app key:      current (%s)\n", strings.Join(d.current, ", "))
	}
}

// appKeyDrift is what the keys this checkout ships say about one environment,
// platform by platform.
type appKeyDrift struct {
	env        string              // the environment compared
	current    []string            // platforms whose key is the one the project hands out
	stale      []string            // platforms whose key the project no longer hands out
	missing    []string            // platforms with configs here, none of them for env
	keyless    []string            // platforms whose config for env carries no key
	unreadable []unreadableConfigs // platforms whose configs could not be read
	err        error               // the project could not be asked
}

// unreadableConfigs is the platforms whose configs failed to read with one
// error — every platform at once when the environments directory itself cannot
// be listed, so the reason is said once rather than once per platform.
type unreadableConfigs struct {
	platforms []string
	err       error
}

// measureAppKeys compares env's key on every platform with a config on disk,
// in knownPlatforms' order, and asks the project once.
//
// EVERY ENVIRONMENT IS ON DISK after a link, so the key to compare is the one
// of the environment this command resolved; with none, the one a build without
// a choice uses — decided over every platform's environments together, so one
// report speaks of one environment.
//
// A PLATFORM WHOSE CONFIG CANNOT BE READ IS KEPT, with the read error: skipping
// it would let the platforms that did read answer "current" for a checkout
// whose other build nobody compared.
func measureAppKeys(ctx context.Context, target Target, env string) appKeyDrift {
	d := appKeyDrift{env: env}
	onDisk := map[string]appEnvironments{}
	var platforms, names []string
	for _, platform := range knownPlatforms {
		envs, err := readAppEnvironments(platform)
		if err != nil {
			d.unreadableAt(platform, err)
			continue
		}
		if len(envs.Environments) == 0 {
			continue
		}
		onDisk[platform] = envs
		platforms = append(platforms, platform)
		names = append(names, envs.names()...)
	}
	if len(platforms) == 0 {
		return d
	}
	if d.env == "" {
		d.env = diskDefault(names)
	}
	var keyed []string
	for _, platform := range platforms {
		entry, ok := onDisk[platform].Environments[d.env]
		switch {
		case !ok:
			d.missing = append(d.missing, platform)
		case entry.APIKey == "":
			d.keyless = append(d.keyless, platform)
		default:
			keyed = append(keyed, platform)
		}
	}
	if len(keyed) == 0 {
		return d
	}
	current, err := projectPublishableKey(ctx, target)
	if err != nil {
		d.err = err
		return d
	}
	for _, platform := range keyed {
		if onDisk[platform].Environments[d.env].APIKey == current {
			d.current = append(d.current, platform)
		} else {
			d.stale = append(d.stale, platform)
		}
	}
	return d
}

// unreadableAt records that platform's configs could not be read, beside any
// platform that failed with the same error.
func (d *appKeyDrift) unreadableAt(platform string, err error) {
	for i := range d.unreadable {
		if d.unreadable[i].err.Error() == err.Error() {
			d.unreadable[i].platforms = append(d.unreadable[i].platforms, platform)
			return
		}
	}
	d.unreadable = append(d.unreadable, unreadableConfigs{platforms: []string{platform}, err: err})
}

// statusAsJSON answers the same questions the text output does, without the
// advice: a script cannot follow "run palbase push", and prose inside a JSON
// document would make it unparseable.
func statusAsJSON(ctx context.Context, cmd *cobra.Command, target Target, keyEnv string, cred Credentials, source string) error {
	doc := statusJSON{
		Project:    target.Describe(),
		Address:    target.URL,
		Credential: statusCredential{Source: source, Kind: credentialKindWord(cred.Kind)},
	}

	status, body, err := managementCall(ctx, target, cred, http.MethodGet,
		"/v1/management/deployments/current", nil, "")
	switch {
	case err != nil:
		return err
	case status == http.StatusNotFound && target.Local:
		doc.Reason = "this stack serves this directory, and rebuilds when you save"
	case status == http.StatusNotFound:
		doc.Reason = "nothing deployed yet"
	case status != http.StatusOK:
		return fmt.Errorf("%s answered %d: %s", target.Describe(), status, trimBody(body))
	default:
		var deployed deploymentState
		if err := json.Unmarshal(body, &deployed); err != nil {
			return fmt.Errorf("read the deployment: %w", err)
		}
		doc.Deployed = &deployed
	}

	doc.AppKey = appKeyState(ctx, target, keyEnv)
	sdkCtx, cancelSDK := context.WithTimeout(ctx, 10*time.Second)
	if running, sErr := projectSDKVersion(sdkCtx, target, cred); sErr == nil {
		doc.SDK = running
	}
	cancelSDK()
	fmt.Fprintln(cmd.OutOrStdout(), renderJSON(doc))
	return nil
}

// appKeyState is reportKeyDrift's finding as one word.
//
// "unchecked" covers both "this checkout ships no key" and "the project could
// not be asked" — a script that must not run against a stale key treats them the
// same, and calling either of them "current" is the failure this reports. So
// one stale platform makes the answer "stale", and "current" needs every
// platform on disk compared and current — a platform whose config could not be
// read was not compared.
func appKeyState(ctx context.Context, target Target, env string) string {
	d := measureAppKeys(ctx, target, env)
	switch {
	case len(d.stale) > 0:
		return "stale"
	case len(d.current) > 0 && len(d.missing) == 0 && len(d.keyless) == 0 && len(d.unreadable) == 0:
		return "current"
	default:
		return "unchecked"
	}
}
