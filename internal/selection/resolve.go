package selection

import (
	"context"
	"errors"
	"fmt"
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
func (p ProjectDetail) RepositoryProvider() (string, error) {
	switch p.Mode {
	case "github":
		return ProviderGitHub, nil
	case "platform":
		return ProviderPalbase, nil
	default:
		return "", fmt.Errorf("project %s has unsupported repository mode %q", p.ID, p.Mode)
	}
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

// EnvironmentRef is the selected Environment's wire ref.
func (s Selection) EnvironmentRef() string { return s.Environment.Ref }

// Resolver turns (--project, --environment, .palbase/config.json) into a
// Selection.
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

	cached *Selection
}

// Config loads the local config. It returns ErrNotSelected when nothing is
// linked and no --project override exists.
func (r *Resolver) Config() (*Config, error) { return Load(r.Dir) }

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
	cfg, err := r.Config()
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
		cfg, err = r.Config()
		if err != nil {
			return Selection{}, err
		}
		projectID = cfg.ProjectID
	}
	detail, err := GetProject(ctx, rest, projectID)
	if err != nil {
		return Selection{}, fmt.Errorf("get project %s: %w", projectID, err)
	}
	provider, err := detail.RepositoryProvider()
	if err != nil {
		return Selection{}, err
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
	best, _ := DefaultEnvironment(envs)
	return best, nil
}

// DefaultEnvironment applies the same deterministic default as Studio:
// production, then the oldest durable Environment, then a preview.
func DefaultEnvironment(envs []Environment) (Environment, bool) {
	if len(envs) == 0 {
		return Environment{}, false
	}
	best := envs[0]
	for _, candidate := range envs[1:] {
		if environmentLess(candidate, best) {
			best = candidate
		}
	}
	return best, true
}

func environmentLess(left, right Environment) bool {
	leftClass, rightClass := environmentClass(left), environmentClass(right)
	if leftClass != rightClass {
		return leftClass < rightClass
	}
	if left.CreatedAt != right.CreatedAt {
		if left.CreatedAt == "" {
			return false
		}
		if right.CreatedAt == "" {
			return true
		}
		return left.CreatedAt < right.CreatedAt
	}
	return left.Ref < right.Ref
}

func environmentClass(e Environment) int {
	if e.IsProduction || e.Kind == "production" {
		return 0
	}
	if e.Ephemeral || e.Kind == "preview" {
		return 2
	}
	return 1
}

func slugList(envs []Environment) string {
	out := make([]string, 0, len(envs))
	for _, e := range envs {
		out = append(out, e.Slug)
	}
	return strings.Join(out, ", ")
}
