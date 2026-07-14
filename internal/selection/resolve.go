package selection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	project, env, err := findEnvironmentByRef(ctx, rest, legacy.Ref)
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

// findEnvironmentByRef locates the Environment that owns a bare ref across every
// Project the caller can see. This is the ONE place the CLI still translates a
// ref, and it exists only to migrate a v1 config.
func findEnvironmentByRef(ctx context.Context, rest REST, ref string) (Project, Environment, error) {
	var projects []Project
	if err := rest.Do(ctx, http.MethodGet, "/api/v2/projects", nil, &projects); err != nil {
		return Project{}, Environment{}, fmt.Errorf("list projects: %w", err)
	}
	for _, p := range projects {
		envs, err := ListEnvironments(ctx, rest, p.ID)
		if err != nil {
			return Project{}, Environment{}, err
		}
		for _, e := range envs {
			if e.Ref == ref {
				return p, e, nil
			}
		}
	}
	return Project{}, Environment{}, fmt.Errorf(
		"no environment with ref %q is visible to you — the project may have been deleted, "+
			"or you are logged in as a different user; re-select with `palbase project use <projectId>`", ref)
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
