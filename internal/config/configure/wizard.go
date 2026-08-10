package configure

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api"
	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	apiiam "github.com/tidbcloud/ti-cli/internal/api/iam"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/config/envcompat"
	"github.com/tidbcloud/ti-cli/internal/config/region"
	"github.com/tidbcloud/ti-cli/internal/config/store"
	"github.com/tidbcloud/ti-cli/internal/secretinput"
)

type Options struct {
	Profile             string
	HomeDir             string
	RegionCode          string
	TiDBCloudPublicKey  string
	TiDBCloudPrivateKey string
	NonInteractive      bool
	Env                 map[string]string
	In                  io.Reader
	Out                 io.Writer
	Resolver            endpoints.Resolver
	Transport           http.RoundTripper
	Timeout             time.Duration
	Debug               bool
	DebugWriter         io.Writer
}

type Result struct {
	Profile           string `json:"profile"`
	RegionCode        string `json:"region_code"`
	ProjectID         string `json:"project_id"`
	ProjectType       string `json:"project_type"`
	CredentialsStored bool   `json:"credentials_stored"`
}

func (r Result) Human() string {
	return fmt.Sprintf("Profile: %s\nRegion: %s\nProject: %s (%s)\nCredentials stored: %t", r.Profile, r.RegionCode, r.ProjectID, r.ProjectType, r.CredentialsStored)
}

const (
	virtualProjectType = "tidbx_virtual"
	projectPageSize    = 100
)

type projectLister interface {
	ListProjects(context.Context, apiiam.ListProjectsOptions) (apiiam.ListProjectsResponse, error)
}

func Run(ctx context.Context, opts Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if opts.Profile == "" {
		opts.Profile = config.DefaultProfile
	}
	if err := config.ValidateProfileName(opts.Profile); err != nil {
		return Result{}, err
	}
	if opts.HomeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Result{}, apperr.Wrap("config.home_dir", "config", 1, "cannot determine home directory", err)
		}
		opts.HomeDir = home
	}
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	input := opts.In
	if !secretinput.IsTerminal(opts.In) {
		input = bufio.NewReader(opts.In)
	}

	defaultRegion := region.DefaultPlacementCode()
	regionInput, err := valueOrEnv(opts.RegionCode, opts.Env, "TI_REGION_CODE")
	if err != nil {
		return Result{}, err
	}
	regionCode, err := valueOrPrompt(ctx, input, opts.Out, regionInput, "default region code", fmt.Sprintf("Default region code (%s): ", defaultRegion), defaultRegion, false, opts.NonInteractive)
	if err != nil {
		return Result{}, err
	}
	placement, err := region.ParsePlacementCode(regionCode)
	if err != nil {
		return Result{}, apperr.Wrap("config.invalid_region", "config", 2, err.Error(), err)
	}

	publicKeyInput, err := valueOrEnv(opts.TiDBCloudPublicKey, opts.Env, "TIDB_CLOUD_PUBLIC_KEY")
	if err != nil {
		return Result{}, err
	}
	publicKey, err := valueOrPrompt(ctx, input, opts.Out, publicKeyInput, "TiDB Cloud public key", "TiDB Cloud public key: ", "", false, opts.NonInteractive)
	if err != nil {
		return Result{}, err
	}
	privateKeyInput, err := valueOrEnv(opts.TiDBCloudPrivateKey, opts.Env, "TIDB_CLOUD_PRIVATE_KEY")
	if err != nil {
		return Result{}, err
	}
	privateKey, err := valueOrPrompt(ctx, input, opts.Out, privateKeyInput, "TiDB Cloud private key", "TiDB Cloud private key: ", "", true, opts.NonInteractive)
	if err != nil {
		return Result{}, err
	}

	profile := &config.Profile{
		Name:                opts.Profile,
		PlacementRegionCode: placement.Code,
		CloudProvider:       placement.Provider,
		RegionCode:          placement.NativeCode,
		TiDBCloudPublicKey:  publicKey,
		TiDBCloudPrivateKey: privateKey,
	}
	project, err := discoverVirtualProject(ctx, opts, profile)
	if err != nil {
		return Result{}, err
	}

	if err := store.WriteProfile(opts.HomeDir, opts.Profile, store.ConfigProfile{
		RegionCode: placement.Code,
		ProjectID:  project.ID,
	}, store.CredentialsProfile{
		TiDBCloudPublicKey:  publicKey,
		TiDBCloudPrivateKey: privateKey,
	}); err != nil {
		return Result{}, apperr.Wrap("config.write_failed", "config", 1, err.Error(), err)
	}

	return Result{
		Profile:           opts.Profile,
		RegionCode:        placement.Code,
		ProjectID:         project.ID,
		ProjectType:       project.Type,
		CredentialsStored: true,
	}, nil
}

func discoverVirtualProject(ctx context.Context, opts Options, profile *config.Profile) (apiiam.Project, error) {
	resolver := opts.Resolver
	if resolver.IsZero() {
		resolver = endpoints.NewResolver()
	}
	endpoint, err := resolver.ResolveIAM()
	if err != nil {
		return apiiam.Project{}, err
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client, err := api.NewDigestClient(profile, endpoint, authz.OrganizationProjectRead, api.Options{
		Action:      "discover default virtual project",
		Transport:   opts.Transport,
		Timeout:     timeout,
		Debug:       opts.Debug,
		DebugWriter: opts.DebugWriter,
		UserAgent:   "ti configure",
	})
	if err != nil {
		return apiiam.Project{}, err
	}
	return selectVirtualProject(ctx, apiiam.New(client))
}

func selectVirtualProject(ctx context.Context, client projectLister) (apiiam.Project, error) {
	if client == nil {
		return apiiam.Project{}, apperr.New("config.project_client_missing", "config", 1, "internal project discovery client is missing")
	}
	var matches []apiiam.Project
	matchedIDs := map[string]struct{}{}
	seenTokens := map[string]struct{}{}
	pageToken := ""
	for {
		response, err := client.ListProjects(ctx, apiiam.ListProjectsOptions{PageSize: projectPageSize, PageToken: pageToken})
		if err != nil {
			return apiiam.Project{}, err
		}
		for _, project := range response.Projects {
			if project.Type != virtualProjectType {
				continue
			}
			projectID := strings.TrimSpace(project.ID)
			if projectID == "" {
				return apiiam.Project{}, apperr.New("config.invalid_virtual_project", "api", 1, "TiDB Cloud returned a tidbx_virtual project without an id")
			}
			if _, exists := matchedIDs[projectID]; exists {
				continue
			}
			project.ID = projectID
			matchedIDs[projectID] = struct{}{}
			matches = append(matches, project)
		}
		next := strings.TrimSpace(response.NextPageToken)
		if next == "" {
			break
		}
		if _, exists := seenTokens[next]; exists {
			return apiiam.Project{}, apperr.New("config.repeated_project_page_token", "api", 1, "TiDB Cloud project pagination returned a repeated page token")
		}
		seenTokens[next] = struct{}{}
		pageToken = next
	}

	if len(matches) == 0 {
		return apiiam.Project{}, apperr.New("config.virtual_project_not_found", "config", 2, "no tidbx_virtual project is available for this TiDB Cloud account")
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for _, project := range matches {
			ids = append(ids, project.ID)
		}
		sort.Strings(ids)
		return apiiam.Project{}, apperr.New("config.virtual_project_ambiguous", "config", 2, fmt.Sprintf("multiple tidbx_virtual projects are available: %s", strings.Join(ids, ", ")))
	}
	return matches[0], nil
}

func valueOrPrompt(ctx context.Context, in io.Reader, out io.Writer, value, fieldName, prompt, defaultValue string, secret, nonInteractive bool) (string, error) {
	if value != "" {
		return strings.TrimSpace(value), nil
	}
	if nonInteractive {
		return "", apperr.New(
			"config.non_interactive_missing",
			"config",
			2,
			fmt.Sprintf("%s is required for non-interactive configure; provide a flag or TI_* environment variable", fieldName),
		)
	}

	value, err := secretinput.Read(ctx, prompt, in, out, secret)
	if err != nil {
		return "", err
	}
	if value == "" {
		if defaultValue != "" {
			return defaultValue, nil
		}
		return "", apperr.New("config.required_input", "config", 2, fieldName+" is required")
	}
	return strings.TrimSpace(value), nil
}

func valueOrEnv(value string, env map[string]string, key string) (string, error) {
	if value != "" {
		return value, nil
	}
	resolved, _, _, err := envcompat.ResolveNames(env, key, envcompat.LegacyNameFor(key))
	return resolved, err
}
