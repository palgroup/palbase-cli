package backend

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The refusal's two lines. The first names the root; the second says what a
// copy in this directory does to a build there — with plugin 2.5.0 it is read
// instead of the checkout's own (a Gradle root holding palbase/project.json is
// a checkout of its own to the plugin, which then never looks above it), or it
// is found together with the checkout's own and the build refuses the two
// ("more than one palbase/environments in reach").
const (
	nestedLinkWhere = " (palbase/project.json) — run `palbase link` there; --platform names this app's platform if it is not found from there.\n"
	nestedLinkNew   = "  A palbase/ written here would be a second copy: a build in this directory would read it instead of the checkout's own, " +
		"or find both and refuse"
	nestedLinkOld = "  The palbase/ here, from an earlier link, is a second copy: a build in this directory reads it instead of the checkout's own, " +
		"or finds both and refuses — delete it and commit that deletion"
)

// linkedMonorepo makes the current directory a repository whose root is linked
// to the backend — `.git` and palbase/project.json, as `palbase link` in the
// backend leaves it — and returns its absolute path and the committed file.
func linkedMonorepo(t *testing.T) (root string, project []byte) {
	t.Helper()
	require.NoError(t, os.Mkdir(".git", 0o755))
	require.NoError(t, os.MkdirAll(RootDir(), 0o755))
	writeFile(t, projectPath(), `{"project":"prd_backend","name":"todo-backend"}`)
	root, err := os.Getwd()
	require.NoError(t, err)
	project, err = os.ReadFile(projectPath())
	require.NoError(t, err)
	return root, project
}

// assertRootUntouched says the linked root kept its own project.json byte for
// byte and gained no environments: a link below it wrote only where it ran.
func assertRootUntouched(t *testing.T, root string, project []byte) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(projectPath())))
	require.NoError(t, err)
	assert.Equal(t, string(project), string(got), "the linked root's project.json changed")
	assert.NoDirExists(t, filepath.Join(root, RootDir(), "environments"), "the link wrote into the linked root")
}

// linkCommandIn runs `palbase link` with args, as a person types it, in the
// current directory, and returns what it failed with.
func linkCommandIn(t *testing.T, args []string) error {
	t.Helper()
	cmd := newLinkCmd(Resolvers{})
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd.Execute()
}

// A LINK IN A GRADLE DIRECTORY BELOW A LINKED CHECKOUT IS REFUSED, AND THE ROOT
// IS NAMED (FR-016).
//
// link wrote wherever it ran (link_artifacts.go `root, err := os.Getwd()`), and
// an Android module is a directory with a build.gradle.kts — detection found an
// app there. `palbase link` inside app/ wrote app/palbase/, and the Gradle
// plugin read that module copy before the checkout's own: measured, the debug
// APK carried `base_url = https://STALE-module-copy…` and the build was green
// (verification D3a).
func TestALinkBelowALinkedCheckoutIsRefusedAndNamesItsRoot(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, os.Mkdir(".git", 0o755))
	o := productLink(t, "todoapp")
	require.NoError(t, runLink(context.Background(), o, io.Discard))
	require.FileExists(t, projectPath())
	root, err := os.Getwd()
	require.NoError(t, err)

	require.NoError(t, os.Chdir("app"))
	var out strings.Builder
	err = runLink(context.Background(), o, &out)

	assert.NoDirExists(t, RootDir(), "a second palbase/ was written inside the module")
	require.Error(t, err)
	assert.Equal(t, filepath.Join(root, "app")+" is inside the checkout linked at "+root+nestedLinkWhere+nestedLinkNew, err.Error())
	assert.Empty(t, out.String())
}

// SO IS A GRADLE ROOT RIGHT BELOW THE LINKED CHECKOUT. React Native and Flutter
// keep the Gradle build in android/, beside ios/ (FR-015), and the plugin looks
// one level above its Gradle root (FR-205): the checkout's own palbase/ is the
// one that build reads, and a copy in android/ would be found before it.
func TestAReactNativeAndroidDirectoryIsRefusedBelowItsLinkedRoot(t *testing.T) {
	inScratchCheckout(t)
	root, project := linkedMonorepo(t)
	require.NoError(t, os.Mkdir("android", 0o755))
	writeFile(t, filepath.Join("android", "settings.gradle"), "include ':app'\n")
	require.NoError(t, os.Chdir("android"))
	o := productLink(t, "todoapp")

	err := runLink(context.Background(), o, io.Discard)

	require.Error(t, err)
	assert.Contains(t, err.Error(), filepath.Join(root, "android")+" is inside the checkout linked at "+root+" (palbase/project.json)")
	assert.NoDirExists(t, RootDir(), "a second palbase/ was written in the Gradle root")
	assertRootUntouched(t, root, project)
}

// THE REACT NATIVE CHECKOUT, AS IT IS LINKED AND THEN LINKED AGAIN IN THE WRONG
// PLACE. `palbase link` at the root finds the Android app in android/ (FR-015)
// and writes palbase/ at the root, one level above the Gradle root the build
// runs in, which is where the plugin reads it (FR-205). A later `palbase link`
// run inside android/ wrote android/palbase/project.json, and that file makes
// android/ the checkout to the plugin: the copy above it left the search and
// the one in android/ was read instead, with no refusal (plugin 2.5.0 README:
// "Run `palbase link` at the checkout root only"). The CLI refuses that link —
// run directly, and as a person types it, with and without a target — names
// the React Native root, and writes nothing in android/.
func TestALinkInsideAReactNativeAndroidDirectoryIsRefusedAndNamesTheRoot(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, os.Mkdir(".git", 0o755))
	writeFile(t, "package.json", `{"name":"rnapp"}`)
	seedGradleFile(t, filepath.Join("android", "settings.gradle"), "include ':app'\n")
	seedGradleFile(t, filepath.Join("android", "build.gradle"), "buildscript {}\n")
	seedGradleFile(t, filepath.Join("android", "app", "build.gradle"), "android {\n  defaultConfig {\n    applicationId \"com.rn.app\"\n  }\n}\n")
	main := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL})
	o := linkOpts{
		url:          main.URL,
		linkedEnv:    "main",
		product:      Product{ID: "prd_rn", Name: "rnapp"},
		environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}},
	}
	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.True(t, strings.HasPrefix(out.String(), "▸ android\n"), out.String())
	root, err := os.Getwd()
	require.NoError(t, err)
	project, err := os.ReadFile(projectPath())
	require.NoError(t, err)
	config, err := os.ReadFile(ConfigPath("main", "android"))
	require.NoError(t, err)
	want := filepath.Join(root, "android") + " is inside the checkout linked at " + root + nestedLinkWhere + nestedLinkNew

	require.NoError(t, os.Chdir("android"))
	err = runLink(context.Background(), o, io.Discard)
	require.Error(t, err)
	assert.Equal(t, want, err.Error())
	assert.NoDirExists(t, RootDir(), "the link inside android/ wrote a second palbase/ there")
	for _, args := range [][]string{{}, {main.URL}} {
		err := linkCommandIn(t, args)
		require.Error(t, err, "%q", args)
		assert.Equal(t, want, err.Error(), "%q", args)
		assert.NoDirExists(t, RootDir(), "%q: `palbase link` inside android/ wrote a second palbase/ there", args)
	}

	gotProject, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(projectPath())))
	require.NoError(t, err)
	assert.Equal(t, string(project), string(gotProject), "the React Native root's project.json changed")
	gotConfig, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ConfigPath("main", "android"))))
	require.NoError(t, err)
	assert.Equal(t, string(config), string(gotConfig), "the React Native root's Android config changed")
}

// A GRADLE ROOT FURTHER DOWN IS A CHECKOUT OF ITS OWN, even inside a repository
// whose root is linked to the backend: the plugin searches apps/android/palbase
// and one level up, never the repository root two levels away, so refusing
// here left the app nowhere a build could find its environments — on the first
// link and on every one after it. Its own module is still a module: app/ below
// it is refused, and the checkout named is the app's.
func TestAnAppThatIsItsOwnGradleRootLinksInsideALinkedMonorepo(t *testing.T) {
	inScratchCheckout(t)
	root, project := linkedMonorepo(t)
	app := filepath.Join(root, "apps", "android")
	require.NoError(t, os.MkdirAll(app, 0o755))
	require.NoError(t, os.Chdir(app))
	writeFile(t, "settings.gradle.kts", "include(\":app\")\n")
	o := productLink(t, "todoapp")

	for _, run := range []string{"link", "re-link"} {
		var out strings.Builder
		require.NoError(t, runLink(context.Background(), o, &out), "%s:\n%s", run, out.String())
		assert.FileExists(t, ConfigPath("main", "android"), run)
		assert.FileExists(t, projectPath(), run)
	}
	assertRootUntouched(t, root, project)

	require.NoError(t, os.Chdir("app"))
	err := runLink(context.Background(), o, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), filepath.Join(app, "app")+" is inside the checkout linked at "+app+" (palbase/project.json)")
	assert.NoDirExists(t, RootDir(), "a second palbase/ was written inside the module")
}

// A GRADLE ROOT WITH A `.git` OF ITS OWN IS A CHECKOUT OF ITS OWN, as the
// plugin's search above a root project stops there (FR-205): a clone kept in
// another project's directory is linked where it is — here a Gradle root right
// below the linked directory, which without its `.git` is refused.
func TestARepositoryInsideALinkedDirectoryLinksOnItsOwn(t *testing.T) {
	inScratchCheckout(t)
	root, project := linkedMonorepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join("sample", ".git"), 0o755))
	require.NoError(t, os.Chdir("sample"))
	writeFile(t, "settings.gradle.kts", "include(\":app\")\n")
	o := productLink(t, "todoapp")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.FileExists(t, ConfigPath("main", "android"))
	assertRootUntouched(t, root, project)
}

// A DIRECTORY WITH NO GRADLE BUILD IS LINKED WHERE IT IS, AS BEFORE (D-025).
// FR-016 is about the Gradle plugin, which reads palbase/ from above the
// directory it builds; a monorepo's web app (package.json) and iOS app (an
// Xcode project) have no build that does, and a link in each of them worked
// before the refusal existed (20e5d7e). Refusing them would take away a working
// link to prevent a copy nothing reads first. Both runs are the same as there.
func TestAnAppWithNoGradleBuildLinksInsideALinkedMonorepoAsBefore(t *testing.T) {
	for _, c := range []struct {
		platform string
		dir      string
		seed     func(t *testing.T)
	}{
		{webPlatform, "apps/web", func(t *testing.T) { seedWebCheckout(t); installStubCodegen(t, "export {}") }},
		{"ios", "apps/ios", func(t *testing.T) {
			require.NoError(t, os.Mkdir("App.xcodeproj", 0o755))
			writeFile(t, filepath.Join("App.xcodeproj", "project.pbxproj"), "SDKROOT = iphoneos;\nPRODUCT_BUNDLE_IDENTIFIER = com.example.app;\n")
			useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
		}},
	} {
		t.Run(c.platform, func(t *testing.T) {
			inScratchCheckout(t)
			root, project := linkedMonorepo(t)
			require.NoError(t, os.MkdirAll(filepath.FromSlash(c.dir), 0o755))
			require.NoError(t, os.Chdir(filepath.FromSlash(c.dir)))
			c.seed(t)
			main := stackServing(t, linkKeyMain, nil)
			routeEnvironments(t, map[string]string{"mainref000": main.URL})
			o := linkOpts{
				url:          main.URL,
				linkedEnv:    "main",
				product:      Product{ID: "prd_a", Name: "todoapp"},
				environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}},
			}

			for _, run := range []string{"link", "re-link"} {
				var out strings.Builder
				require.NoError(t, runLink(context.Background(), o, &out), "%s:\n%s", run, out.String())
				assert.True(t, strings.HasPrefix(out.String(), "▸ "+c.platform+"\n"), "%s:\n%s", run, out.String())
				assert.FileExists(t, ConfigPath("main", c.platform), run)
				assert.FileExists(t, projectPath(), run)
			}
			assertRootUntouched(t, root, project)
		})
	}
}

// AND SO IS ONE WITH NO APP AT ALL: docs/ below the linked root binds the
// backend only, as it did before the refusal existed (20e5d7e).
func TestADirectoryWithNoGradleBuildBelowALinkedCheckoutLinksAsBefore(t *testing.T) {
	inScratchCheckout(t)
	root, project := linkedMonorepo(t)
	require.NoError(t, os.Mkdir("docs", 0o755))
	require.NoError(t, os.Chdir("docs"))
	main := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL})
	o := linkOpts{
		url:          main.URL,
		linkedEnv:    "main",
		product:      Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.True(t, strings.HasPrefix(out.String(), "▸ no client app here"), out.String())
	assert.FileExists(t, projectPath())
	assert.NoDirExists(t, EnvDir("main"))
	assertRootUntouched(t, root, project)
}

// A MODULE IS A MODULE EVEN WHEN IT IS A REPOSITORY OF ITS OWN. The plugin
// reads a module's palbase/ together with its Gradle root's whatever `.git`
// sits between them — the `.git` bound is the root project's alone (FR-205) —
// so an app module kept as a submodule inside a linked Android checkout would
// hold the second copy all the same.
func TestAModuleWithAGitOfItsOwnIsStillAModule(t *testing.T) {
	inScratchCheckout(t)
	root, project := linkedMonorepo(t)
	writeFile(t, "settings.gradle.kts", "include(\":app\")\n")
	o := productLink(t, "todoapp")
	writeFile(t, filepath.Join("app", ".git"), "gitdir: ../.git/modules/app\n")
	require.NoError(t, os.Chdir("app"))

	err := runLink(context.Background(), o, io.Discard)

	require.Error(t, err)
	assert.Contains(t, err.Error(), filepath.Join(root, "app")+" is inside the checkout linked at "+root+" (palbase/project.json)")
	assert.NoDirExists(t, RootDir(), "a second palbase/ was written inside the module")
	assertRootUntouched(t, root, project)
}

// THE WALK IS WHERE THE DIRECTORY IS ON DISK. A shell that reached android/
// through a symlink names it by the link, and the link's parent is not the
// directory the plugin looks in: Gradle resolves a build's directories, and
// the React Native root above android/ is where that build reads palbase/.
func TestAGradleDirectoryReachedThroughASymlinkIsWalkedWhereItIs(t *testing.T) {
	inScratchCheckout(t)
	root, project := linkedMonorepo(t)
	seedGradleFile(t, filepath.Join("android", "settings.gradle"), "include ':app'\n")
	shortcut := filepath.Join(t.TempDir(), "android")
	require.NoError(t, os.Symlink(filepath.Join(root, "android"), shortcut))
	require.NoError(t, os.Chdir(shortcut))
	t.Setenv("PWD", shortcut) // a shell's `cd` keeps the name it was given
	o := productLink(t, "todoapp")

	err := runLink(context.Background(), o, io.Discard)

	require.Error(t, err)
	assert.Contains(t, err.Error(), " is inside the checkout linked at "+root+" (palbase/project.json)")
	assert.NoDirExists(t, filepath.Join(root, "android", RootDir()), "a second palbase/ was written through the symlink")
	assertRootUntouched(t, root, project)
}

// A COPY AN EARLIER LINK LEFT HERE IS NAMED, NOT PROMISED AWAY. An older CLI
// linked wherever it ran, and in a React Native checkout android/ was the only
// place it found Android (FR-015): the copy this rule keeps from being born may
// be here already. Its palbase/project.json makes android/ a checkout of its
// own to the plugin, which then never looks above it (FR-205) — linking at the
// root, as the refusal says, would leave the build on the old copy, in silence,
// unless the refusal says to delete it.
func TestARefusalNamesTheCopyAnEarlierLinkLeftHere(t *testing.T) {
	inScratchCheckout(t)
	root, project := linkedMonorepo(t)
	seedGradleFile(t, filepath.Join("android", "settings.gradle"), "include ':app'\n")
	old := filepath.Join(root, "android", "palbase", "environments", "main", "android-config.json")
	seedGradleFile(t, filepath.Join("android", "palbase", "project.json"), `{"project":"prd_a","name":"todoapp"}`)
	seedGradleFile(t, old, `{"base_url":"https://stale.example"}`)
	require.NoError(t, os.Chdir("android"))
	o := productLink(t, "todoapp")

	err := runLink(context.Background(), o, io.Discard)

	require.Error(t, err)
	assert.Equal(t, filepath.Join(root, "android")+" is inside the checkout linked at "+root+nestedLinkWhere+nestedLinkOld, err.Error())
	got, readErr := os.ReadFile(old)
	require.NoError(t, readErr)
	assert.Equal(t, `{"base_url":"https://stale.example"}`, string(got), "the refusal touched the old copy")
	assertRootUntouched(t, root, project)
}

// THE REFUSAL COMES BEFORE THE TARGET IS RESOLVED. With no target, `palbase
// link` reads the record in the directory it runs in — the copy an older link
// left there — and lists that project's environments; with a project's name
// it lists the projects. Asked in that order, a person with no session was
// sent to `palbase login` first, and one with a session had the cloud asked,
// before either was told this is the wrong directory.
func TestTheRefusalComesBeforeTheTargetIsResolved(t *testing.T) {
	inScratchCheckout(t)
	root, _ := linkedMonorepo(t)
	seedGradleFile(t, filepath.Join("android", "settings.gradle"), "include ':app'\n")
	seedGradleFile(t, filepath.Join("android", "palbase", "project.json"), `{"project":"prd_a","name":"todoapp"}`)
	require.NoError(t, os.Chdir("android"))
	listed := 0
	prev := EnvironmentsOf
	EnvironmentsOf = func(context.Context, string) ([]Environment, error) {
		listed++
		return nil, errors.New("the project's environments were listed")
	}
	t.Cleanup(func() { EnvironmentsOf = prev })

	for _, args := range [][]string{{}, {"todoapp"}} {
		err := linkCommandIn(t, args)

		require.Error(t, err, "%q", args)
		assert.True(t, strings.HasPrefix(err.Error(), filepath.Join(root, "android")+" is inside the checkout linked at "+root+" "), "%q: %v", args, err)
	}
	assert.Zero(t, listed, "the cloud was asked before the refusal")
}
