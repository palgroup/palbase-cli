package backend

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/config"
)

func twoProjects() *nameREST {
	return &nameREST{rows: []map[string]any{
		{"id": "prd_a", "name": "todoapp", "environments": []map[string]any{
			{"ref": "8qitbtucm", "name": "main", "status": "Running"},
			{"ref": "mu0028xyz", "name": "staging", "status": "Running"},
		}},
		{"id": "prd_b", "name": "centauri", "environments": []map[string]any{
			{"ref": "8bbwb2pbm", "name": "main", "status": "Running"},
		}},
	}}
}

func TestAnEnvironmentRefNamesItsProject(t *testing.T) {
	product, envs, err := productByName(context.Background(), resolvers(twoProjects()), "mu0028xyz")
	require.NoError(t, err)
	assert.Equal(t, "prd_a", product.ID)
	assert.Equal(t, "todoapp", product.Name)
	assert.Len(t, envs, 2)
}

func TestAnUnknownTokenListsTheProjects(t *testing.T) {
	_, _, err := productByName(context.Background(), resolvers(twoProjects()), "nosuchref")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "todoapp")
	assert.Contains(t, err.Error(), "centauri")
}

func TestATokenThatIsTwoProjectsIsRefused(t *testing.T) {
	rest := twoProjects()
	rest.rows = append(rest.rows, map[string]any{
		"id": "prd_c", "name": "8qitbtucm", "environments": []map[string]any{},
	})
	_, _, err := productByName(context.Background(), resolvers(rest), "8qitbtucm")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prd_a")
	assert.Contains(t, err.Error(), "prd_c")
}

// A CLOUD ENVIRONMENT'S ADDRESS IS ITS PROJECT'S NAME SPELLED LONGER.
func TestACloudEnvironmentAddressBindsTheProject(t *testing.T) {
	inScratchCheckout(t)
	prev := CloudProjectAddress
	t.Cleanup(func() { CloudProjectAddress = prev })
	CloudProjectAddress = func(u string) bool { return u == "https://mu0028xyz.palbase.studio" }

	o := linkOpts{url: "https://mu0028xyz.palbase.studio"}
	r := Resolvers{
		REST:      func() REST { return twoProjects() },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "palbase.studio"} },
	}
	require.NoError(t, resolveLinkTarget(context.Background(), r, &o))
	assert.Equal(t, "prd_a", o.product.ID)
	assert.Len(t, o.environments, 2)
	assert.Equal(t, "main", o.linkedEnv, "the address names the project; the default rule picks the environment")
	assert.Equal(t, "https://8qitbtucm.palbase.studio", o.url, "the link acts on the DEFAULT environment's address")
}

// FR-001 + FR-003 through the link itself: the record is the project, and no
// machine-local selection is written.
func TestLinkingAProductWritesTheProjectAndNoSelection(t *testing.T) {
	inScratchCheckout(t)
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	o := linkOpts{url: srv.URL, product: Product{ID: "prd_a", Name: "todoapp"}, linkedEnv: "main"}
	require.NoError(t, runLink(context.Background(), o, io.Discard))

	record, err := readLinkedProject()
	require.NoError(t, err)
	assert.Equal(t, "prd_a", record.Project)
	assert.Equal(t, "todoapp", record.Name)
	assert.Empty(t, record.URL, "a project record carries no address")
	_, selErr := ReadSelection(".")
	assert.Error(t, selErr, "a link wrote a machine-local selection")
}

// AN ADDRESS THE LISTING DOES NOT KNOW STAYS AN ADDRESS (FR-005): the control
// plane's own stack is served under the same suffix and operators link it.
func TestAnAddressWithNoMatchingEnvironmentIsLeftAlone(t *testing.T) {
	inScratchCheckout(t)
	prev := CloudProjectAddress
	t.Cleanup(func() { CloudProjectAddress = prev })
	CloudProjectAddress = func(string) bool { return true }

	o := linkOpts{url: "https://api.palbase.studio"}
	r := Resolvers{
		REST:      func() REST { return twoProjects() },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "palbase.studio"} },
	}
	require.NoError(t, resolveLinkTarget(context.Background(), r, &o))
	assert.Empty(t, o.product.ID)
	assert.Equal(t, "https://api.palbase.studio", o.url)
}

func TestAnAddressWithTokenStdinIsLeftAlone(t *testing.T) {
	inScratchCheckout(t)
	prev := CloudProjectAddress
	t.Cleanup(func() { CloudProjectAddress = prev })
	CloudProjectAddress = func(string) bool { return true }

	o := linkOpts{url: "https://mu0028xyz.palbase.studio", tokenStdin: true}
	r := Resolvers{
		REST:      func() REST { return twoProjects() },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "palbase.studio"} },
	}
	require.NoError(t, resolveLinkTarget(context.Background(), r, &o))
	assert.Empty(t, o.product.ID)
}

func TestLinkHelpNamesEveryForm(t *testing.T) {
	cmd := newLinkCmd(Resolvers{})
	for _, want := range []string{
		"palbase link <project>",
		"palbase link <ref>",
		"palbase link https://<ref>.",
		"--token-stdin",
		"palbase link\n",
	} {
		assert.Contains(t, cmd.Long, want)
	}
}

// A LISTING THAT CANNOT BE READ LEAVES THE ADDRESS ALONE (FR-005). An operator
// with no session links the control plane's own stack by address, and a person
// whose session expired still reaches the credential message — neither is
// refused because the projects could not be listed.
func TestAnAddressWhoseListingCannotBeReadIsLeftAlone(t *testing.T) {
	inScratchCheckout(t)
	prev := CloudProjectAddress
	t.Cleanup(func() { CloudProjectAddress = prev })
	CloudProjectAddress = func(string) bool { return true }

	o := linkOpts{url: "https://mu0028xyz.palbase.studio"}
	r := Resolvers{
		REST:      func() REST { return &nameREST{err: errors.New("401 unauthorized")} },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "palbase.studio"} },
	}
	require.NoError(t, resolveLinkTarget(context.Background(), r, &o))
	assert.Empty(t, o.product.ID)
	assert.Equal(t, "https://mu0028xyz.palbase.studio", o.url)
}

// AN ADDRESS OUTSIDE THIS CLOUD IS NEVER LOOKED UP: whose projects there are
// says nothing about a stack somebody runs.
func TestAnAddressOutsideTheCloudIsNotLookedUp(t *testing.T) {
	inScratchCheckout(t)
	prev := CloudProjectAddress
	t.Cleanup(func() { CloudProjectAddress = prev })
	CloudProjectAddress = func(string) bool { return false }

	rest := twoProjects()
	o := linkOpts{url: "https://stack.example.com"}
	r := Resolvers{
		REST:      func() REST { return rest },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "palbase.studio"} },
	}
	require.NoError(t, resolveLinkTarget(context.Background(), r, &o))
	assert.Empty(t, rest.path, "the listing was asked about an address outside this cloud")
	assert.Equal(t, "https://stack.example.com", o.url)
}

// A HOST IS CASE-INSENSITIVE. An environment address pasted in capitals is the
// same environment, and binding it as an address would commit the retired
// record shape.
func TestAnEnvironmentAddressInCapitalsBindsTheProject(t *testing.T) {
	inScratchCheckout(t)
	prev := CloudProjectAddress
	t.Cleanup(func() { CloudProjectAddress = prev })
	CloudProjectAddress = func(string) bool { return true }

	o := linkOpts{url: "https://MU0028XYZ.palbase.studio"}
	r := Resolvers{
		REST:      func() REST { return twoProjects() },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "palbase.studio"} },
	}
	require.NoError(t, resolveLinkTarget(context.Background(), r, &o))
	assert.Equal(t, "prd_a", o.product.ID)
}

// A RECORD FROM BEFORE PROJECTS, LINKED AGAIN WITH NO TARGET, binds the project
// its address belongs to — the rule typing that address already follows. It
// used to take the address path and write the retired record shape back.
func TestANoTargetLinkBindsTheProjectOfARecordFromBeforeProjects(t *testing.T) {
	inScratchCheckout(t)
	prev := CloudProjectAddress
	t.Cleanup(func() { CloudProjectAddress = prev })
	CloudProjectAddress = func(u string) bool { return u == "https://mu0028xyz.palbase.studio" }
	require.NoError(t, WriteTarget(Target{URL: "https://mu0028xyz.palbase.studio"}))

	o := linkOpts{}
	r := Resolvers{
		REST:      func() REST { return twoProjects() },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "palbase.studio"} },
	}
	require.NoError(t, resolveLinkTarget(context.Background(), r, &o))
	assert.Equal(t, "prd_a", o.product.ID)
	assert.Equal(t, "https://8qitbtucm.palbase.studio", o.url, "the link acts on the default environment's address")
}

// unboundCloudAddress serves a project at a loopback address this test declares
// to be under the cloud's tenant host, with no credential for it anywhere.
func unboundCloudAddress(t *testing.T) (string, string) {
	t.Helper()
	inScratchCheckout(t)
	t.Setenv(AccessTokenEnv, "")
	srv := stackServing(t, linkKeyMain, nil)
	prev := CloudProjectAddress
	t.Cleanup(func() { CloudProjectAddress = prev })
	CloudProjectAddress = func(u string) bool { return u == srv.URL }
	return srv.URL, strings.ToLower(refOfURL(srv.URL))
}

func listingResolvers(rest *nameREST) Resolvers {
	return Resolvers{
		REST:      func() REST { return rest },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "palbase.studio"} },
	}
}

// THE CAUSE BEFORE THE ADVICE (X-12). Measured on 0.67.1: a listing that failed
// once came out as "no credential for this project" for an address the account
// owned, and the next attempt, seconds later, linked.
func TestAnUnboundCloudAddressNamesTheListingFailure(t *testing.T) {
	address, _ := unboundCloudAddress(t)
	o := linkOpts{url: address}
	require.NoError(t, resolveLinkTarget(context.Background(),
		listingResolvers(&nameREST{err: errors.New("503 listing unavailable")}), &o))

	err := runLink(context.Background(), o, io.Discard)
	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, "503 listing unavailable")
	require.Contains(t, msg, "no credential for this project")
	assert.Less(t, strings.Index(msg, "503 listing unavailable"), strings.Index(msg, "no credential for this project"),
		"the credential advice came before the reason the address was not bound")
}

func TestACloudAddressNoProjectListsNamesItsRef(t *testing.T) {
	address, ref := unboundCloudAddress(t)
	o := linkOpts{url: address}
	require.NoError(t, resolveLinkTarget(context.Background(),
		listingResolvers(&nameREST{rows: []map[string]any{}}), &o))

	err := runLink(context.Background(), o, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), ref+" is not an environment of any project this account can see")
	assert.Contains(t, err.Error(), "palbase project list")
}

// A KEY IS STILL A WAY IN. An address the listing did not bind but a stored key
// opens links exactly as before: the control plane's own stack is linked so.
func TestAnUnboundCloudAddressWithAKeyStillLinks(t *testing.T) {
	address, _ := unboundCloudAddress(t)
	linkedAs(t, address, "pb_project_opens-it")
	o := linkOpts{url: address}
	require.NoError(t, resolveLinkTarget(context.Background(),
		listingResolvers(&nameREST{err: errors.New("503")}), &o))

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	assert.Contains(t, out.String(), "linked to "+address)
}
