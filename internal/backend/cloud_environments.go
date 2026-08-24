package backend

// cloud_environments.go — a CLOUD-linked app holds every environment too.
//
// The multi-environment model already existed for a stack you host yourself
// (`palbase link <url>`, app_environments.go): the link downloads every
// environment, the plist carries them all, one xcconfig per environment decides
// which one a build talks to, and each gets its own generated client. The design
// note there ends with *"the two meet when the management API exists on both
// sides"* — and the cloud half had none, so `palbase ios link` kept writing a
// SINGLE environment and the app in your hands was whichever one somebody linked
// last.
//
// The management API exists now. This file is the cloud half of
// `gatherEnvironments`: the app's bindings name its environments, the config
// artifact gives each one its address and publishable key, and the contract
// comes per environment because environments serve different contracts — a
// client merged across them would compile calls that do not exist where the app
// is pointed.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/palgroup/palbase-cli/internal/apps"
)

// cloudEnvDeps are the seams the cloud gather needs, injected so the whole
// sequence is testable without a live plane.
type cloudEnvDeps struct {
	fetch      specFetch
	list       bindingLister
	cfgFetch   configArtifactFetch
	freshness  specFreshness
	publicHost string
}

// gatherCloudEnvironments collects every environment this app can be built
// against: each one it is BOUND to, plus the stack running on this machine.
//
// selectedRef is the environment the caller chose, and it becomes the default —
// the one a build with no PALBASE_ENV gets. It must be among the bindings: an
// app pointed at an environment it is not bound to would be configured with
// another environment's key, which is precisely the failure this refuses.
func gatherCloudEnvironments(
	ctx context.Context,
	d cloudEnvDeps,
	appID, selectedRef, group string,
	w io.Writer,
) (appEnvironments, map[string][]byte, error) {
	bindings, err := d.list(ctx, appID)
	if err != nil {
		return appEnvironments{}, nil, fmt.Errorf("list app %q bindings: %w", appID, err)
	}

	selected := ""
	for i := range bindings {
		if bindings[i].EnvironmentRef == selectedRef {
			selected = environmentName(bindings[i])
			break
		}
	}
	if selected == "" {
		return appEnvironments{}, nil, fmt.Errorf(
			"app %q is not bound to environment %q — run the platform link command again", appID, selectedRef)
	}

	envs := appEnvironments{Default: selected, Environments: map[string]appEnvironment{}}
	specs := map[string][]byte{}

	for i := range bindings {
		name := environmentName(bindings[i])
		ref := bindings[i].EnvironmentRef
		art, err := d.cfgFetch(ctx, appID, ref)
		if err != nil {
			return appEnvironments{}, nil, fmt.Errorf("fetch config artifact for %s: %w", ref, err)
		}
		// THE ARTIFACT IS UNTRUSTED INPUT AT THIS LAYER, and it is judged HERE —
		// before its URL selects a network target or its key reaches a file.
		// The production fetcher validates on the way through too; that is not a
		// duplicate but the same rule at the layer that USES the value, which is
		// the only layer that can promise it.
		if err := apps.ValidateConfigArtifact(art, appID, ref, d.publicHost); err != nil {
			return appEnvironments{}, nil, err
		}
		entry := appEnvironment{AppID: appID, BaseURL: art.BaseURL, APIKey: art.APIKey}
		if art.OAuth != nil {
			raw, err := json.Marshal(art.OAuth)
			if err != nil {
				return appEnvironments{}, nil, fmt.Errorf("encode oauth for %s: %w", ref, err)
			}
			entry.OAuth = raw
		}
		envs.Environments[name] = entry

		// THE CONTRACT IS FETCHED PER ENVIRONMENT, and a failure on a NON-selected
		// one does not stop the link: an environment whose backend is down still
		// deserves its config entry and its build configuration — dropping it
		// would make a build configuration vanish because a pod was restarting.
		// The selected one is different: it is the environment being linked, and
		// writing no contract for it would leave the checkout unable to build.
		spec, err := fetchEnvironmentSpec(ctx, d, ref, name == selected, w)
		if err != nil {
			return appEnvironments{}, nil, err
		}
		if spec == nil {
			continue
		}
		if err := writeSpec(name, spec); err != nil {
			return appEnvironments{}, nil, err
		}
		specs[name] = spec
	}

	addLocalStack(ctx, envs, specs, group, "", w)
	return envs, specs, nil
}

// fetchEnvironmentSpec reads one environment's contract. A non-selected
// environment reports its failure and yields nil rather than failing the link.
func fetchEnvironmentSpec(
	ctx context.Context, d cloudEnvDeps, ref string, isSelected bool, w io.Writer,
) ([]byte, error) {
	// Freshness is checked for the SELECTED environment only: it is the one
	// being built against, and holding a link open while some other environment
	// finishes a rollout would block work that does not depend on it.
	var freshness specFreshness
	if isSelected {
		freshness = d.freshness
	}
	spec, _, err := fetchFreshSpec(ctx, d.fetch, ref, freshness, w)
	if err != nil {
		if isSelected {
			return nil, err
		}
		fmt.Fprintf(w, "  note: %s has no contract right now (%v) — its build configuration is written without a client\n", ref, err)
		return nil, nil
	}
	return spec, nil
}

// environmentName is what an environment is called in a checkout: a directory
// under Palbase/Generated, an xcconfig, and a build configuration.
//
// The plane answers with a name (`main` for a project's one environment); a
// blank one falls back to the ref so two environments can never collide into a
// single directory and silently overwrite each other's client.
func environmentName(b AppBinding) string {
	name := strings.TrimSpace(b.EnvironmentName)
	if name == "" {
		return b.EnvironmentRef
	}
	return name
}

// addLocalStack adds the stack running on this machine, when there is one.
//
// It is added WITHOUT a key when the stack is registered but not answering,
// rather than left out: an app whose Local configuration disappears because a
// container was stopped is an app that stops compiling for a reason nobody
// connects to the container. The entry says what to run instead.
func addLocalStack(
	ctx context.Context,
	envs appEnvironments,
	specs map[string][]byte,
	group, excludeURL string,
	w io.Writer,
) {
	localURL := LookupLocalStack(group)
	if localURL == "" || localURL == excludeURL {
		return
	}
	localTarget := Target{URL: localURL, Local: true}
	localCred, _, credErr := Credential(localURL)
	if credErr != nil {
		envs.Environments[localEnvName] = appEnvironment{AppID: projectAppID, BaseURL: localURL}
		fmt.Fprintf(w, "local: %s is registered but this machine holds no credential for it — `palbase start`\n", localURL)
		return
	}
	localKey, keyErr := projectPublishableKey(ctx, localTarget)
	if keyErr != nil {
		envs.Environments[localEnvName] = appEnvironment{AppID: projectAppID, BaseURL: localURL}
		fmt.Fprintf(w, "local: %s did not answer — run `palbase start`, then `palbase spec` to fill it in\n", localURL)
		return
	}
	envs.Environments[localEnvName] = appEnvironment{
		AppID:   projectAppID,
		BaseURL: localURL,
		APIKey:  localKey,
	}
	if localSpec, err := fetchStackSpec(ctx, localTarget, localCred); err == nil {
		if err := writeSpec(localEnvName, localSpec); err == nil {
			specs[localEnvName] = localSpec
		}
	}
}

// writeNativeEnvironments writes what a native checkout carries for EVERY
// environment: the config the SDK reads, one build configuration each, and one
// generated client each.
func writeNativeEnvironments(
	ctx context.Context,
	platform string,
	envs appEnvironments,
	specs map[string][]byte,
	w io.Writer,
) error {
	if err := os.MkdirAll(nativeArtifactsDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", nativeArtifactsDir, err)
	}
	// THE OLD FLAT CONTRACT GOES. One environment used to mean one
	// `.palbase/openapi.json`; leaving it beside the per-environment ones leaves
	// a committed contract nothing regenerates, and the next person to read the
	// directory cannot tell which file the client came from. It is OUR file and
	// it is generated content, so it is removed rather than left to be found.
	legacy := filepath.Join(nativeArtifactsDir, "openapi.json")
	if err := os.Remove(legacy); err == nil {
		fmt.Fprintf(w, "removed %s (one contract per environment now)\n", legacy)
	} else if !os.IsNotExist(err) {
		return err
	}
	path, err := writeAppEnvironments(platform, envs)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "✓ wrote %s (%s)\n", path, strings.Join(envs.names(), ", "))

	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := pruneRemovedEnvironments(root, envs, w); err != nil {
		return err
	}
	if err := writeXcconfigs(root, envs, w); err != nil {
		return err
	}
	reportInfoPlistRequirement(root, envs, w)
	if err := generateForEnvironments(ctx, envs, w); err != nil {
		return err
	}
	reportContractDrift(specs, w)
	return nil
}

// linkNativeEnvironments is the whole cloud sequence for one platform.
func linkNativeEnvironments(
	ctx context.Context,
	d cloudEnvDeps,
	platform, appID, selectedRef, group string,
	w io.Writer,
) error {
	envs, specs, err := gatherCloudEnvironments(ctx, d, appID, selectedRef, group, w)
	if err != nil {
		return err
	}
	return writeNativeEnvironments(ctx, platform, envs, specs, w)
}

// pruneRemovedEnvironments deletes the generated content of environments this
// checkout no longer has.
//
// AN ORPHAN STILL COMPILES, and that is the whole problem. The xcconfig excludes
// the OTHER known environments by name, so a directory left behind by an
// environment that was renamed or removed is excluded by nothing — Xcode 16's
// synchronized groups compile every file under the folder, and the build fails
// with "Multiple commands produce PalbaseGenerated.stringsdata" or, worse,
// silently links a client for an address that is gone. Measured on this very
// checkout: an environment named by its ref left `Palbase/Generated/8bbwb2pbm/`
// beside `main/` the moment the plane learned to name it.
//
// Only OUR generated shapes are touched: a per-environment client directory, its
// contract, and its build configuration. Nothing a person wrote is removed.
func pruneRemovedEnvironments(root string, envs appEnvironments, w io.Writer) error {
	keep := map[string]bool{}
	for _, name := range envs.names() {
		keep[name] = true
	}

	entries, err := os.ReadDir(filepath.Join(root, generatedDir))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || keep[e.Name()] {
			continue
		}
		// A directory is ours only if it holds the file we generate. Anything
		// else under Palbase/Generated belongs to somebody and stays.
		client := filepath.Join(root, generatedDir, e.Name(), "PalbaseGenerated.swift")
		if _, statErr := os.Stat(client); statErr != nil {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, generatedDir, e.Name())); err != nil {
			return err
		}
		fmt.Fprintf(w, "removed %s (no such environment)\n", filepath.Join(generatedDir, e.Name()))
		if err := removeIfPresent(specPath(e.Name())); err != nil {
			return err
		}
		if err := removeIfPresent(filepath.Join(root, "Palbase", "Config", xcconfigName(e.Name()))); err != nil {
			return err
		}
	}
	return nil
}

func removeIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
