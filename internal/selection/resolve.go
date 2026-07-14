package selection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// REST is the Management-API transport subset the resolver needs.
// *transport.Client satisfies it; tests substitute an httptest-backed stub.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Project mirrors a `GET /api/v2/projects` row. The Project has NO ref, NO tier
// and NO region: entitlement lives on its Organization and the endpoint lives
// on its Environments.
type Project struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	Name           string `json:"name"`
	CreatedAt      string `json:"created_at"`
	AppsRequired   bool   `json:"apps_required"`
}

// ProjectDetail is `GET /api/v2/projects/{projectId}` — the Project plus the
// caller's role and its ONE repository.
type ProjectDetail struct {
	Project
	Role       string `json:"role"`
	GithubRepo string `json:"github_repo"`
	Mode       string `json:"mode"` // "github" | "platform"
}

// RepositoryProvider maps the server's `mode` onto the config's
// `repository_provider` vocabulary (spec §4: palbase | github).
func (p ProjectDetail) RepositoryProvider() string {
	if p.Mode == "github" {
		return ProviderGitHub
	}
	return ProviderPalbase
}

// Environment mirrors a `GET /api/v2/projects/{projectId}/environments` row.
//
// `Ref` is the wire identity — the endpoint / DNS label / API-key ref, globally
// unique. Every runtime route and tRPC procedure addresses an Environment BY
// REF; the config stores the ID (the stable identity) and the resolver turns
// one into the other.
type Environment struct {
	ID              string  `json:"id"`
	ProjectID       string  `json:"project_id"`
	Ref             string  `json:"ref"`
	Name            string  `json:"name"`
	Slug            string  `json:"slug"`
	Kind            string  `json:"kind"`
	IsProduction    bool    `json:"is_production"`
	Ephemeral       bool    `json:"ephemeral"`
	Region          string  `json:"region"`
	Status          string  `json:"status"`
	DesiredState    string  `json:"desired_state"`
	SourceGitBranch *string `json:"source_git_branch"`
	CreatedAt       string  `json:"created_at"`
}

// Selection is the resolved context one command acts on.
type Selection struct {
	ProjectID          string
	Environment        Environment
	RepositoryProvider string
}

// Ref is the selected Environment's wire ref.
func (s Selection) Ref() string { return s.Environment.Ref }

// Resolver turns (--project, --environment, .palbase/config.json) into a
// Selection, migrating a v1 config in place on first use.
//
// It caches: a command that needs the Project and the Environment resolves both
// with ONE environments listing.
type Resolver struct {
	// REST is the Management-API client (lazy: main.go builds it per invocation).
	REST func() REST
	// ProjectFlag / EnvironmentFlag are the global --project / --environment
	// headless overrides. EnvironmentFlag matches a ref, slug, or name.
	ProjectFlag     string
	EnvironmentFlag string
	// Dir is the directory holding .palbase/config.json ("" = cwd).
	Dir string
	// Warn receives progress + migration notices. It MUST be stderr: stdout is
	// reserved for stable, parseable command output.
	Warn io.Writer

	cached *Selection
}

func (r *Resolver) warn(format string, a ...any) {
	if r.Warn == nil {
		return
	}
	fmt.Fprintf(r.Warn, format, a...)
}

// Config loads the local config, migrating a v1 file in place. It returns
// ErrNotSelected when nothing is linked and no --project override exists.
func (r *Resolver) Config(ctx context.Context) (*Config, error) {
	cfg, err := Load(r.Dir)
	if err == nil {
		return cfg, nil
	}
	var legacy *ErrLegacyConfig
	if errors.As(err, &legacy) {
		return r.migrate(ctx, legacy)
	}
	return nil, err
}

// migrate resolves a v1 config's bare ref to {project_id, environment_id} over
// the v2 API and rewrites the file. It never 404s silently: an unresolvable ref
// is a hard error naming the ref, because the alternative is every subsequent
// command failing with an opaque "project not found".
func (r *Resolver) migrate(ctx context.Context, legacy *ErrLegacyConfig) (*Config, error) {
	rest, err := r.rest()
	if err != nil {
		return nil, err
	}
	project, env, err := findEnvironmentByRef(ctx, rest, legacy.Ref, legacy.legacy.DefaultEnv)
	if err != nil {
		return nil, fmt.Errorf("migrate %s to config v2: %w", ConfigPath(legacy.Dir), err)
	}

	provider := ProviderPalbase
	if legacy.legacy.Mode == "github" || legacy.legacy.GithubRepo != "" {
		provider = ProviderGitHub
	}
	cfg := &Config{
		Version:            Version,
		ProjectID:          project.ID,
		EnvironmentID:      env.ID,
		RepositoryProvider: provider,
		IOSAppID:           legacy.legacy.IOSAppID,
		MacOSAppID:         legacy.legacy.MacOSAppID,
		WebAppID:           legacy.legacy.WebAppID,
		AndroidAppID:       legacy.legacy.AndroidAppID,
	}
	if err := Save(legacy.Dir, cfg); err != nil {
		return nil, err
	}
	r.warn("migrated %s to config v2: ref %q → project %s, environment %s (%s)\n",
		ConfigPath(legacy.Dir), legacy.Ref, project.ID, env.ID, env.Slug)
	return cfg, nil
}

// findEnvironmentByRef locates the Environment a v1 config's `ref` names, across
// every Project the caller can see. This is the ONE place the CLI still translates
// a ref, and it exists only to migrate a v1 config.
//
// A v1 `ref` is NOT an environment ref. It named the old env-CONTAINER ("project")
// row, and the endpoint of a branch under it was `<ref><branchSlug>`. Migration 129
// folded container+branch into ONE environment whose ref IS that old endpoint ref —
// so a v1 `{"ref":"todoappm8p6z","default_env":"main"}` corresponds to the
// environment `todoappm8p6zm`. Matching on equality (what this did) therefore found
// NOTHING for every real v1 config on disk, and `palbase status` died with "no
// environment with ref ... is visible to you".
//
// So: an exact match still wins (a config already holding an env ref), and otherwise
// the ref is treated as the BASE and matched against `<ref><slug>` — where the slug
// must be the one the environment's own KIND implies, never any old suffix, so a
// neighbouring project (`app12m` when we hold `app1`) cannot be mistaken for ours.
//
// `defaultEnv` is the v1 branch selector; it only breaks a TIE between several
// environments of the same base ref. An unresolvable tie is an ERROR that lists the
// candidates — silently picking one would point `push` at the wrong environment.
func findEnvironmentByRef(ctx context.Context, rest REST, ref, defaultEnv string) (Project, Environment, error) {
	var projects []Project
	if err := rest.Do(ctx, http.MethodGet, "/api/v2/projects", nil, &projects); err != nil {
		return Project{}, Environment{}, fmt.Errorf("list projects: %w", err)
	}

	type candidate struct {
		project Project
		env     Environment
	}
	var candidates []candidate
	for _, p := range projects {
		envs, err := ListEnvironments(ctx, rest, p.ID)
		if err != nil {
			return Project{}, Environment{}, err
		}
		for _, e := range envs {
			if e.Ref == ref {
				return p, e, nil // already an environment ref: nothing to translate
			}
			if envRefMatchesBase(e, ref) {
				candidates = append(candidates, candidate{p, e})
			}
		}
	}

	switch len(candidates) {
	case 0:
		return Project{}, Environment{}, fmt.Errorf(
			"no environment with ref %q is visible to you — the project may have been deleted, "+
				"or you are logged in as a different user; re-select with `palbase project use <projectId>`", ref)
	case 1:
		return candidates[0].project, candidates[0].env, nil
	}

	// Several environments share the base ref (the old branches). The v1
	// `default_env` is the only thing that says which one this directory meant.
	var matched []candidate
	for _, c := range candidates {
		if legacyEnvMatches(c.env, defaultEnv) {
			matched = append(matched, c)
		}
	}
	if len(matched) == 1 {
		return matched[0].project, matched[0].env, nil
	}

	names := make([]string, 0, len(candidates))
	for _, c := range candidates {
		names = append(names, fmt.Sprintf("%s (%s, project %s)", c.env.Ref, c.env.Kind, c.project.ID))
	}
	return Project{}, Environment{}, fmt.Errorf(
		"ref %q (default_env %q) matches %d environments — %s; re-select explicitly with "+
			"`palbase project use <projectId>`",
		ref, defaultEnv, len(candidates), strings.Join(names, ", "))
}

// envRefMatchesBase reports whether e is an environment of the base ref `base` —
// i.e. its ref is `base` + the ref slug its OWN kind implies.
//
// Deriving the slug from the kind (rather than accepting any suffix) is what keeps
// a plain prefix scan from grabbing a NEIGHBOUR: with base "app1", the project
// "app12"'s production environment is "app12m", whose leftover suffix "2m" is not a
// slug of any kind, so it is correctly rejected.
func envRefMatchesBase(e Environment, base string) bool {
	suffix, ok := strings.CutPrefix(e.Ref, base)
	if !ok || suffix == "" {
		return false
	}
	if want := envRefSlugForKind(e.Kind); want != "" {
		return suffix == want
	}
	// preview: the slug carries the PR number (p7, p42) and is not derivable from
	// the kind — accept exactly that shape.
	return previewSlug.MatchString(suffix)
}

// envRefSlugForKind mirrors the server's EnvRefSlugForKind (orchestrator
// internal/workflows/types.go): environments.ref is `<baseRef><slug>`, and the slug
// comes from the kind. "" = not derivable (preview, or a kind we do not know).
func envRefSlugForKind(kind string) string {
	switch kind {
	case "", "production":
		return "m"
	case "staging":
		return "s"
	case "dev":
		return "d"
	case "preprod":
		return "pp"
	default:
		return ""
	}
}

var previewSlug = regexp.MustCompile(`^p[0-9]+$`)

// legacyEnvMatches reports whether env e is the one a v1 `default_env` named.
//
// v1 `default_env` was a Palbase BRANCH name. `main` was the branch every project
// was born with and the one the cutover folded into the PRODUCTION environment, so
// that is what it maps to; any other value is matched against the environment's own
// kind/slug/name (`staging` -> the staging environment).
func legacyEnvMatches(e Environment, defaultEnv string) bool {
	switch defaultEnv {
	case "":
		return false
	case "main":
		return e.IsProduction
	}
	return e.Kind == defaultEnv || e.Slug == defaultEnv || e.Name == defaultEnv
}

// ListEnvironments is `GET /api/v2/projects/{projectId}/environments`.
func ListEnvironments(ctx context.Context, rest REST, projectID string) ([]Environment, error) {
	var envs []Environment
	if err := rest.Do(ctx, http.MethodGet, "/api/v2/projects/"+projectID+"/environments", nil, &envs); err != nil {
		return nil, fmt.Errorf("list environments of %s: %w", projectID, err)
	}
	return envs, nil
}

// GetProject is `GET /api/v2/projects/{projectId}`.
func GetProject(ctx context.Context, rest REST, projectID string) (ProjectDetail, error) {
	var p ProjectDetail
	if err := rest.Do(ctx, http.MethodGet, "/api/v2/projects/"+projectID, nil, &p); err != nil {
		return ProjectDetail{}, err
	}
	return p, nil
}

func (r *Resolver) rest() (REST, error) {
	if r.REST == nil {
		return nil, errors.New("management API client is not wired")
	}
	c := r.REST()
	if c == nil {
		return nil, errors.New("management API client is not wired")
	}
	return c, nil
}

// ProjectID resolves ONLY the Project: --project wins, else the local config.
// Commands that act on the Project (apps, members, project settings) use this
// and never pay for an environments listing.
func (r *Resolver) ProjectID(ctx context.Context) (string, error) {
	if r.ProjectFlag != "" {
		return r.ProjectFlag, nil
	}
	cfg, err := r.Config(ctx)
	if err != nil {
		return "", err
	}
	return cfg.ProjectID, nil
}

// Resolve produces the full (Project, Environment) context.
//
// Precedence: --project / --environment > .palbase/config.json. --environment
// accepts a ref, a slug, or a display name so `--environment staging` works
// without the user knowing the ref. With a Project but no Environment selected,
// production is the default — a Project always has exactly one.
func (r *Resolver) Resolve(ctx context.Context) (Selection, error) {
	if r.cached != nil {
		return *r.cached, nil
	}
	rest, err := r.rest()
	if err != nil {
		return Selection{}, err
	}

	var cfg *Config
	projectID := r.ProjectFlag
	if projectID == "" {
		cfg, err = r.Config(ctx)
		if err != nil {
			return Selection{}, err
		}
		projectID = cfg.ProjectID
	}

	envs, err := ListEnvironments(ctx, rest, projectID)
	if err != nil {
		return Selection{}, err
	}
	if len(envs) == 0 {
		return Selection{}, fmt.Errorf("project %s has no environments", projectID)
	}

	wantID := ""
	if cfg != nil {
		wantID = cfg.EnvironmentID
	}
	env, err := pickEnvironment(envs, r.EnvironmentFlag, wantID)
	if err != nil {
		return Selection{}, err
	}

	provider := ProviderPalbase
	if cfg != nil && cfg.RepositoryProvider != "" {
		provider = cfg.RepositoryProvider
	}
	sel := Selection{ProjectID: projectID, Environment: env, RepositoryProvider: provider}
	r.cached = &sel
	return sel, nil
}

// pickEnvironment selects from a Project's Environments. `flag` is the
// --environment override (ref | slug | name); `wantID` is the config's
// environment_id. With neither, production wins.
func pickEnvironment(envs []Environment, flag, wantID string) (Environment, error) {
	if flag != "" {
		for _, e := range envs {
			if e.Ref == flag || e.Slug == flag || strings.EqualFold(e.Name, flag) {
				return e, nil
			}
		}
		return Environment{}, fmt.Errorf("no environment %q in this project — have: %s", flag, slugList(envs))
	}
	if wantID != "" {
		for _, e := range envs {
			if e.ID == wantID {
				return e, nil
			}
		}
		return Environment{}, fmt.Errorf(
			"the selected environment (%s) no longer exists — re-select with `palbase env use <slug>` (have: %s)",
			wantID, slugList(envs))
	}
	for _, e := range envs {
		if e.IsProduction {
			return e, nil
		}
	}
	return Environment{}, fmt.Errorf("no environment selected and this project has no production environment — pick one with `palbase env use <slug>` (have: %s)", slugList(envs))
}

func slugList(envs []Environment) string {
	out := make([]string, 0, len(envs))
	for _, e := range envs {
		out = append(out, e.Slug)
	}
	return strings.Join(out, ", ")
}
