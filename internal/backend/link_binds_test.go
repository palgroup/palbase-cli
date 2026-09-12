package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var linkEnvs = []Environment{
	{Ref: "j06bwtuum", Name: "main", Status: "Running"},
	{Ref: "mu0028", Name: "staging", Status: "Running"},
	{Ref: "aa11bb22c", Name: "canary", Status: "Running"},
}

// LINK BINDS WITH SEVERAL ENVIRONMENTS — FR-004, and the reason the whole
// change exists.
//
// The fail-closed rule belongs to the verbs that CHANGE an environment. `link`
// writes a project identity; the environment it picks is only where it reads
// the contract from. Refusing here made "bind me to the project, I will choose
// the environment when I plan" impossible — measured on the product: a second
// environment appeared and `palbase link linkuat` started refusing.
func TestLinkBindsWhenTheProductHasSeveralEnvironments(t *testing.T) {
	ref, err := linkEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, linkEnvs, "")
	require.NoError(t, err, "link refused a product with several environments")
	require.Equal(t, "j06bwtuum", ref, "with nothing chosen, `main` is the one it reads from")
}

// THE CHOICE IS DETERMINISTIC WITHOUT `main`, so two runs of one command agree.
func TestLinkFallsBackToTheFirstEnvironmentByName(t *testing.T) {
	envs := []Environment{
		{Ref: "zzz", Name: "staging"},
		{Ref: "aaa", Name: "canary"},
	}
	first, err := linkEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, envs, "")
	require.NoError(t, err)
	second, err := linkEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, envs, "")
	require.NoError(t, err)
	require.Equal(t, first, second, "two identical calls disagreed")
	require.Equal(t, "aaa", first, "the order is not the listing's order, which the server may change")
}

// A CHOICE THIS MACHINE ALREADY MADE WINS over the convention.
func TestLinkReadsFromTheEnvironmentThisMachineChose(t *testing.T) {
	root := linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	require.NoError(t, WriteSelection(root, Selection{Project: "prd_a", Env: "staging", Ref: "mu0028"}))

	ref, err := linkEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, linkEnvs, "")
	require.NoError(t, err)
	require.Equal(t, "mu0028", ref, "link ignored the environment this checkout is set to")
}

// A SELECTION FROM ANOTHER PROJECT IS NOT USED.
func TestLinkIgnoresAnotherProjectsSelection(t *testing.T) {
	root := linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	require.NoError(t, WriteSelection(root, Selection{Project: "prd_OTHER", Env: "staging", Ref: "mu0028"}))

	ref, err := linkEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, linkEnvs, "")
	require.NoError(t, err)
	require.Equal(t, "j06bwtuum", ref, "a selection belonging to another project was used")
}

// AN UNKNOWN `--from-env` STILL REFUSES, and lists what does exist: the caller
// named something, so guessing past it would act somewhere they did not name.
func TestLinkRefusesAnEnvironmentNobodyHas(t *testing.T) {
	_, err := linkEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, linkEnvs, "production")
	require.Error(t, err)
	require.Contains(t, err.Error(), "production")
	require.Contains(t, err.Error(), "staging")
}

// A PRODUCT WITH NO ENVIRONMENTS IS REFUSED WITH THE WAY OUT, not with an
// index out of range.
func TestLinkRefusesAProductWithNoEnvironments(t *testing.T) {
	_, err := linkEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, nil, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "palbase env create")
}

// FR-062: THE OLD ARTIFACT DIRECTORY IS NAMED AND LEFT ALONE.
//
// A backend-only checkout used to get `palbase/environments/<env>/` on every
// link — an openapi.json and a roles.json no generator in this product reads —
// and that is what "bu jsonlar falan çok gereksiz change olarak geliyor"
// described. It is not written any more, but the copies already committed do
// not vanish, and a `link` that deleted them would be this CLI reaching into a
// customer's repository. Saying the sentence is the whole fix: without a word,
// the directory reads as current.
func TestTheStaleArtifactDirectoryIsNamedAndKept(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, RootDir(), envSubdir, "main")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openapi.json"), []byte("{}"), 0o644))

	var out bytes.Buffer
	reportStaleArtifacts(&out, root, nil)

	got := out.String()
	require.Contains(t, got, filepath.Join(RootDir(), envSubdir),
		"the directory that is no longer written was not named")
	require.Contains(t, got, "main", "the notice does not say what is in there")
	require.Contains(t, strings.ToLower(got), "nothing writes it")
	require.DirExists(t, dir, "link deleted a directory the customer committed")
}

// AND IT SAYS NOTHING WHEN THERE IS NOTHING TO SAY — a checkout with a
// generator still owns that directory, and an empty one is not news.
func TestNoStaleArtifactNoticeWhenThereIsNothingStale(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	reportStaleArtifacts(&out, root, nil)
	require.Empty(t, out.String(), "a checkout with no such directory was told about one")

	dir := filepath.Join(root, RootDir(), envSubdir, "staging")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	out.Reset()
	reportStaleArtifacts(&out, root, []string{"ios"})
	require.Empty(t, out.String(), "an app checkout was told its own artifact directory is stale")
}

// THE ARTIFACT DIRECTORY'S NAME COMES FROM THE CHOSEN ENVIRONMENT, and this is
// the step the old defect lived in.
//
// `EnvDir("staging")` is measured elsewhere, but that function takes the name
// as an argument — it cannot tell whether the CALLER handed it a constant, and
// handing it a constant is precisely what `defaultEnvName` did for every cloud
// link. So the step between "which ref did we choose" and "what do we call the
// directory" is measured here, including the case with no `main` in sight.
func TestTheArtifactDirectoryTakesTheChosenEnvironmentsName(t *testing.T) {
	require.Equal(t, "staging", envNameOfRef(linkEnvs, "mu0028"))
	require.Equal(t, "canary", envNameOfRef(linkEnvs, "aa11bb22c"),
		"an environment that is not `main` lost its name")

	// AND THE WHOLE CHAIN AGREES: a product with no `main` must not land in
	// `environments/main/`, which is what an empty name becomes downstream.
	noMain := []Environment{{Ref: "zzz", Name: "staging"}, {Ref: "aaa", Name: "canary"}}
	ref, err := linkEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, noMain, "")
	require.NoError(t, err)
	name := envNameOfRef(noMain, ref)
	require.NotEmpty(t, name, "the chosen environment has no name, so the directory becomes a constant")
	require.Equal(t, "palbase/environments/canary", EnvDir(name))
	require.NotContains(t, EnvDir(name), "main")

	// A REF NOBODY HAS HAS NO NAME: the fallback belongs to a stack somebody
	// runs, and inventing a name here would hide a chooser that went wrong.
	require.Empty(t, envNameOfRef(linkEnvs, "notaref"))
}
