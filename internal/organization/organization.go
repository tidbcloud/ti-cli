package organization

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api"
	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	apiiam "github.com/tidbcloud/ti-cli/internal/api/iam"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
)

type Service struct {
	Resolver    endpoints.Resolver
	HTTPClient  *http.Client
	Transport   http.RoundTripper
	Timeout     time.Duration
	Debug       bool
	DebugWriter io.Writer
}

type ListProjectsOptions struct {
	Profile   *config.Profile
	PageSize  int32
	PageToken string
}

type ListProjectsResult struct {
	Projects      []apiiam.Project `json:"projects"`
	NextPageToken string           `json:"next_page_token,omitempty"`
}

func (s Service) ListProjects(ctx context.Context, opts ListProjectsOptions) (ListProjectsResult, error) {
	if opts.PageSize < 0 {
		return ListProjectsResult{}, apperr.New("organization.invalid_page_size", "usage", 2, "--page-size must be greater than or equal to 0")
	}
	if opts.Profile == nil {
		return ListProjectsResult{}, apperr.New("organization.missing_profile", "config", 2, "active profile is required")
	}

	resolver := s.resolver()
	endpoint, err := resolver.ResolveIAM()
	if err != nil {
		return ListProjectsResult{}, err
	}

	client, err := api.NewDigestClient(opts.Profile, endpoint, authz.OrganizationProjectRead, api.Options{
		Action:      "list projects",
		HTTPClient:  s.HTTPClient,
		Transport:   s.Transport,
		Timeout:     s.Timeout,
		Debug:       s.Debug,
		DebugWriter: s.DebugWriter,
		UserAgent:   "ti organization list-projects",
	})
	if err != nil {
		return ListProjectsResult{}, err
	}

	response, err := apiiam.New(client).ListProjects(ctx, apiiam.ListProjectsOptions{
		PageSize:  opts.PageSize,
		PageToken: opts.PageToken,
	})
	if err != nil {
		return ListProjectsResult{}, err
	}

	projects := response.Projects
	if projects == nil {
		projects = []apiiam.Project{}
	}
	return ListProjectsResult{
		Projects:      projects,
		NextPageToken: response.NextPageToken,
	}, nil
}

func (s Service) resolver() endpoints.Resolver {
	if s.Resolver.IsZero() {
		return endpoints.NewResolver()
	}
	return s.Resolver
}

func (r ListProjectsResult) Human() string {
	var out strings.Builder
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "ID\tNAME\tTYPE\tORG_ID\tCLUSTERS\tUSERS\tCREATED\tAWS_CMEK")
	for _, project := range r.Projects {
		_, _ = fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%d\t%d\t%s\t%t\n",
			project.ID,
			project.Name,
			project.Type,
			project.OrgID,
			project.ClusterCount,
			project.UserCount,
			project.CreateTimestamp,
			project.AWSCMEKEnabled,
		)
	}
	if r.NextPageToken != "" {
		_, _ = fmt.Fprintf(writer, "next_page_token\t%s\t\t\t\t\t\t\n", r.NextPageToken)
	}
	_ = writer.Flush()
	return strings.TrimRight(out.String(), "\n")
}
