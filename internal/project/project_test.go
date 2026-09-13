package project

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubREST records what the command asked the control plane for, and answers
// with whatever the test set up.
type stubREST struct {
	method, path string
	body         any
	reply        any
	err          error
	// calls IS AN ASSERTION, not bookkeeping: the listing's whole shape rests
	// on one request, and nothing measured that until this field existed.
	calls int
}

func (s *stubREST) Do(_ context.Context, method, path string, body, out any) error {
	s.calls++
	s.method, s.path, s.body = method, path, body
	if s.err != nil {
		return s.err
	}
	if out == nil || s.reply == nil {
		return nil
	}
	raw, err := json.Marshal(s.reply)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

type stubCloud struct {
	domain string
	err    error
}

func (s stubCloud) TenantDomain(context.Context) (string, error) { return s.domain, s.err }

func resolvers(rest *stubREST, cloud Bootstrapper) Resolvers {
	return Resolvers{
		REST:  func() REST { return rest },
		Cloud: func() Bootstrapper { return cloud },
	}
}

func run(t *testing.T, r Resolvers, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := Cmd(r)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// routeREST answers the two calls create makes — the POST that creates the
// project and the listing that says whether its name is unique among the
// caller's projects — and records the body the POST carried.
type routeREST struct {
	created Tenant
	rows    []Project
	listErr error
	sent    any
}

func (r *routeREST) Do(_ context.Context, method, path string, body, out any) error {
	var reply any
	switch {
	case method == "POST" && path == "/v1/cloud/projects":
		r.sent = body
		reply = r.created
	case method == "GET" && path == "/api/v2/projects":
		if r.listErr != nil {
			return r.listErr
		}
		reply = r.rows
	default:
		return fmt.Errorf("unexpected %s %s", method, path)
	}
	raw, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func routed(rest *routeREST) Resolvers {
	return Resolvers{
		REST:  func() REST { return rest },
		Cloud: func() Bootstrapper { return stubCloud{domain: "palbase.studio"} },
	}
}

func named(s string) *string { return &s }

// CREATING A PROJECT ENDS WITH THE COMMAND THAT LINKS IT — by name. It used to
// print an environment's address, and linking by address wrote the retired
// record shape into the repository.
func TestCreateSuggestsLinkingByName(t *testing.T) {
	rest := &routeREST{
		created: Tenant{Ref: "abc123xyz", Name: named("shop"), Phase: "Running"},
		rows:    []Project{{ID: "prd_a", Name: "shop"}, {ID: "prd_b", Name: "penny"}},
	}
	out, err := run(t, routed(rest), "", "create", "shop")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sent, _ := rest.sent.(map[string]any)
	if sent["name"] != "shop" || sent["tier"] != "free" {
		t.Fatalf("body did not carry the name and default tier: %#v", rest.sent)
	}
	if !strings.Contains(out, "palbase link shop\n") {
		t.Fatalf("create did not suggest linking by name:\n%s", out)
	}
	if strings.Contains(out, "palbase link https://") {
		t.Fatalf("create still suggests an address:\n%s", out)
	}
}

// A NAME TWO PROJECTS SHARE IS REFUSED BY LINK, so create must not suggest it.
func TestCreateSuggestsTheRefWhenTheNameIsShared(t *testing.T) {
	rest := &routeREST{
		created: Tenant{Ref: "abc123xyz", Name: named("shop"), Phase: "Running"},
		rows:    []Project{{ID: "prd_a", Name: "shop"}, {ID: "prd_b", Name: "Shop "}},
	}
	out, err := run(t, routed(rest), "", "create", "shop")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(out, "palbase link abc123xyz") {
		t.Fatalf("two projects share the name, and create did not suggest the ref:\n%s", out)
	}
}

func TestCreateSuggestsTheRefWhenTheListingFails(t *testing.T) {
	rest := &routeREST{
		created: Tenant{Ref: "abc123xyz", Name: named("shop"), Phase: "Running"},
		listErr: fmt.Errorf("unreachable"),
	}
	out, err := run(t, routed(rest), "", "create", "shop")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(out, "palbase link abc123xyz") {
		t.Fatalf("an unread listing cannot vouch for the name, and create did not fall back to the ref:\n%s", out)
	}
}

func TestCreateQuotesANameAShellWouldSplit(t *testing.T) {
	rest := &routeREST{
		created: Tenant{Ref: "abc123xyz", Name: named("Todo App"), Phase: "Running"},
		rows:    []Project{{ID: "prd_a", Name: "Todo App"}},
	}
	out, err := run(t, routed(rest), "", "create", "Todo App")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(out, "palbase link 'Todo App'") {
		t.Fatalf("a name with a space was not quoted for the shell:\n%s", out)
	}
}

// The domain comes from the cloud. When it cannot be read, the command still
// succeeds — the project exists — and never invents a host.
func TestCreateStillSucceedsWhenTheDomainIsUnknown(t *testing.T) {
	rest := &stubREST{reply: Tenant{Ref: "abc123xyz", Phase: "Running"}}
	out, err := run(t, resolvers(rest, stubCloud{err: fmt.Errorf("unreachable")}), "", "create", "shop")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if strings.Contains(out, "https://abc123xyz.v2") {
		t.Fatalf("invented a domain it could not read:\n%s", out)
	}
	// Adsız bir cevapta ref hâlâ görünmeli — ad yoksa satır "(adsız)" der ama
	// kimliği yutmaz.
	if !strings.Contains(out, "abc123xyz") || !strings.Contains(out, "Created") {
		t.Fatalf("did not report the created project:\n%s", out)
	}
	// An unnamed project cannot be linked by name; its ref always resolves.
	if !strings.Contains(out, "palbase link abc123xyz") {
		t.Fatalf("an unnamed project was not suggested by its ref:\n%s", out)
	}
}

// THE LISTING IS PROJECTS WITH THEIR ENVIRONMENTS NESTED, and that shape is
// the point rather than a formatting choice.
//
// Flattened is what it used to be: one row per ENVIRONMENT, each carrying the
// PROJECT's name. So a project with two environments printed the same name
// twice and a reader could not tell a second environment from a second
// project — and `palbase link <name>` could not resolve it at all.
func TestListShowsEveryProjectWithItsEnvironments(t *testing.T) {
	rest := &stubREST{reply: []Project{
		{ID: "prd_a", Name: "centauri", Environments: []Environment{
			{Ref: "aaa", Name: "main", Status: "Running"},
			{Ref: "aab", Name: "staging", Status: "Running"},
		}},
		{ID: "prd_b", Name: "penny", Environments: []Environment{
			{Ref: "bbb", Name: "main", Status: "Pending"},
		}},
	}}
	out, err := run(t, resolvers(rest, stubCloud{}), "", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// ONE CALL, and it is the surface that groups environments under products.
	if rest.path != "/api/v2/projects" {
		t.Fatalf("wrong path: %s", rest.path)
	}
	if rest.calls != 1 {
		t.Fatalf("the listing made %d requests, want 1", rest.calls)
	}
	// THE PROJECT NAME APPEARS ONCE per project, not once per environment.
	if n := strings.Count(out, "centauri"); n != 1 {
		t.Fatalf("the project name appears %d times, want 1 — repeating it reads as two projects:\n%s", n, out)
	}
	// AND BOTH ENVIRONMENTS ARE THERE, by name and by ref.
	for _, want := range []string{"main", "staging", "aaa", "aab"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q missing from:\n%s", want, out)
		}
	}
	// AD DA GÖRÜNMELİ, HÜCRE GÖRÜNMEMELİ.
	for _, want := range []string{"Running", "bbb", "penny"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q missing from:\n%s", want, out)
		}
	}
}

// THE REQUEST COUNT DOES NOT GROW WITH THE NUMBER OF PROJECTS, and this is
// measured on the CLIENT because the client is what makes the requests.
//
// The invariant was written down on the server instead — `CliProjectSchema`'s
// comment says environments are nested rather than served from their own
// endpoint because otherwise "`palbase project list` M projeli bir hesapta M+1
// istek yapardı". That is a statement about THIS code's behaviour living in
// another component's file, where no test of this package can defend it: a
// later "just fetch each project's environments" loop would read as an
// improvement and break the reason the shape exists. Three projects, still one
// request.
func TestTheListingMakesOneRequestNoMatterHowManyProjects(t *testing.T) {
	rest := &stubREST{reply: []Project{
		{ID: "prd_a", Name: "one", Environments: []Environment{{Ref: "a1", Name: "main", Status: "Running"}}},
		{ID: "prd_b", Name: "two", Environments: []Environment{{Ref: "b1", Name: "main", Status: "Running"}}},
		{ID: "prd_c", Name: "three", Environments: []Environment{
			{Ref: "c1", Name: "main", Status: "Running"},
			{Ref: "c2", Name: "staging", Status: "Running"},
		}},
	}}
	if _, err := run(t, resolvers(rest, stubCloud{}), "", "list"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if rest.calls != 1 {
		t.Fatalf("three projects cost %d requests, want 1 — the nesting exists to keep this at 1", rest.calls)
	}
}

// A PROJECT WITH NO ENVIRONMENTS IS STILL LISTED — it exists, and saying
// nothing about it would make it unreachable.
func TestAProjectWithNoEnvironmentsIsStillListed(t *testing.T) {
	rest := &stubREST{reply: []Project{{ID: "prd_a", Name: "fresh", Environments: nil}}}
	out, err := run(t, resolvers(rest, stubCloud{}), "", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "fresh") || !strings.Contains(out, "(none)") {
		t.Fatalf("an environment-less project vanished from the listing:\n%s", out)
	}
}

func TestListSaysSoWhenThereAreNone(t *testing.T) {
	out, err := run(t, resolvers(&stubREST{reply: []Project{}}, stubCloud{}), "", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "No projects yet") {
		t.Fatalf("an empty list printed nothing useful:\n%s", out)
	}
}

// Deleting takes a microVM and its disk with it, so the confirmation must be
// something nobody satisfies by reflex: typing the ref means having read which
// project this is.
func TestDeleteRefusesAMismatchedConfirmation(t *testing.T) {
	rest := &stubREST{}
	_, err := run(t, resolvers(rest, stubCloud{}), "wrong\n", "delete", "abc123xyz")
	if err == nil {
		t.Fatal("a mismatched confirmation was accepted")
	}
	if rest.method != "" {
		t.Fatalf("the delete was sent anyway: %s %s", rest.method, rest.path)
	}
}

func TestDeleteProceedsOnAMatchingConfirmation(t *testing.T) {
	rest := &stubREST{}
	out, err := run(t, resolvers(rest, stubCloud{}), "abc123xyz\n", "delete", "abc123xyz")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if rest.method != "DELETE" || rest.path != "/v1/cloud/projects/abc123xyz" {
		t.Fatalf("wrong call: %s %s", rest.method, rest.path)
	}
	if !strings.Contains(out, "Deleted abc123xyz") {
		t.Fatalf("no confirmation printed:\n%s", out)
	}
}

// --yes is for scripts, and must not silently need a terminal.
func TestDeleteWithYesSkipsThePrompt(t *testing.T) {
	rest := &stubREST{}
	if _, err := run(t, resolvers(rest, stubCloud{}), "", "delete", "abc123xyz", "--yes"); err != nil {
		t.Fatalf("delete --yes: %v", err)
	}
	if rest.method != "DELETE" {
		t.Fatalf("the delete never went out: %#v", rest)
	}
}

// YERLEŞİM MÜŞTERİYE GÖSTERİLMEZ.
//
// Tablo `REF PHASE CELL` basıyordu. Hücre yerleşimin iç detayı: kimseye bir şey
// yaptırmıyor, ama topolojiyi anlatıyor. Yerine İNSANIN VERDİĞİ AD geldi — sekiz
// projesi olan biri sekiz opak ref'e bakıyordu.
func TestStatusNamesTheProject(t *testing.T) {
	name := "centauri"
	rest := &stubREST{reply: Tenant{Ref: "abc123xyz", Name: &name, Phase: "Running"}}
	out, err := run(t, resolvers(rest, stubCloud{}), "", "status", "abc123xyz")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if rest.path != "/v1/cloud/projects/abc123xyz" {
		t.Fatalf("wrong path: %s", rest.path)
	}
	for _, want := range []string{"abc123xyz", "Running", "centauri"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q missing from:\n%s", want, out)
		}
	}
}

// A ref is user input and lands in a URL path. Escaping it here means a ref
// carrying a slash cannot address a different resource.
func TestRefIsEscapedIntoThePath(t *testing.T) {
	rest := &stubREST{reply: Project{}}
	if _, err := run(t, resolvers(rest, stubCloud{}), "", "status", "a/../../admin"); err != nil {
		t.Fatalf("status: %v", err)
	}
	if strings.Contains(rest.path, "../") {
		t.Fatalf("an unescaped ref reached the path: %s", rest.path)
	}
}

// THE STALE SENTENCE MUST NOT COME BACK.
//
// This package's doc comment asserted the opposite of the product for weeks: it
// said a project simply WAS a tenant, with no organisation above it and no
// environment set below. (The literal words live only in the refusal list
// below — writing them in prose here would trip the gate this test IS.) True
// the day it was written, false ever since — the control plane grew `cloud_products` above the tenant
// row and a verb to add a second environment under it.
//
// A doc that contradicts the model is worse than a missing one: it is the thing
// the next reader believes. So the claim is measured, not trusted.
func TestThePackageDocDoesNotDenyEnvironments(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	body, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "project.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{
		"no Environment set below it",
		"A project in the v2 cloud IS a tenant",
	} {
		if strings.Contains(string(body), gone) {
			t.Errorf("the package doc still denies the model it documents: %q", gone)
		}
	}
	// AND IT SAYS THE TRUE THING EXPLICITLY. A gate that only hunts the lie is
	// satisfied by silence; this one demands the sentence.
	if !strings.Contains(string(body), "A PROJECT IS A GROUP OF ENVIRONMENTS") {
		t.Error("the package doc does not state what a project is")
	}
}

// A QUOTE IN THE NAME is where a quoting mistake shows, so the suggestion is
// read back through a real shell: one argument, and exactly the name.
func TestCreateQuotesANameWithAQuoteInIt(t *testing.T) {
	rest := &routeREST{
		created: Tenant{Ref: "abc123xyz", Name: named("it's"), Phase: "Running"},
		rows:    []Project{{ID: "proj_a", Name: "it's"}},
	}
	out, err := run(t, routed(rest), "", "create", "it's")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const prefix = "palbase link "
	i := strings.Index(out, prefix)
	if i < 0 {
		t.Fatalf("no link suggestion:\n%s", out)
	}
	word := strings.TrimSpace(strings.SplitN(out[i+len(prefix):], "\n", 2)[0])
	got, err := exec.Command("sh", "-c", "set -- "+word+"; printf '%s|%s' \"$#\" \"$1\"").Output()
	if err != nil {
		t.Fatalf("a shell could not read %q: %v", word, err)
	}
	if string(got) != "1|it's" {
		t.Fatalf("the shell read %q back from %q, not the name as one argument", got, word)
	}
}

// A NAME THAT IS ANOTHER PROJECT'S REF is two answers to `palbase link`, which
// counts names, ids and refs together — so create suggests the ref.
func TestCreateSuggestsTheRefWhenTheNameIsAlsoAnotherProjectsRef(t *testing.T) {
	rest := &routeREST{
		created: Tenant{Ref: "abc123xyz", Name: named("shop"), Phase: "Running"},
		rows: []Project{
			{ID: "proj_a", Name: "shop"},
			{ID: "proj_b", Name: "penny", Environments: []Environment{{Ref: "shop", Name: "main", Status: "Running"}}},
		},
	}
	out, err := run(t, routed(rest), "", "create", "shop")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(out, "palbase link abc123xyz") {
		t.Fatalf("the name is also another project's ref, and create did not suggest the ref:\n%s", out)
	}
}

// A NAME THAT STARTS WITH A DASH is a flag to the command line unless `--`
// comes before it.
func TestCreateSeparatesANameThatStartsWithADash(t *testing.T) {
	rest := &routeREST{
		created: Tenant{Ref: "abc123xyz", Name: named("-shop"), Phase: "Running"},
		rows:    []Project{{ID: "proj_a", Name: "-shop"}},
	}
	out, err := run(t, routed(rest), "", "create", "--", "-shop")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(out, "palbase link -- -shop") {
		t.Fatalf("a name starting with a dash was suggested without --:\n%s", out)
	}
}

// A NAME WITH "://" IN IT is read by `palbase link` as an address, so it gets
// the ref even when the listing shows exactly one project by it (FR-023).
func TestCreateSuggestsTheRefForANameLinkWouldReadAsAnAddress(t *testing.T) {
	rest := &routeREST{
		created: Tenant{Ref: "abc123xyz", Name: named("a://b"), Phase: "Running"},
		rows:    []Project{{ID: "proj_a", Name: "a://b"}},
	}
	out, err := run(t, routed(rest), "", "create", "a://b")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(out, "palbase link abc123xyz") || strings.Contains(out, "palbase link 'a://b'") {
		t.Fatalf("a name link would read as an address was suggested as the name:\n%s", out)
	}
}
