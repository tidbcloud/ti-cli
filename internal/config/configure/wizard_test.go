package configure

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	apiiam "github.com/tidbcloud/ti-cli/internal/api/iam"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/config/store"
)

func TestRunWritesProfileAndDoesNotPrintSecret(t *testing.T) {
	home := t.TempDir()
	input := strings.NewReader("aws-us-east-1\npublic-key\nprivate-key\n")
	var output bytes.Buffer

	result, err := Run(context.Background(), Options{
		Profile:  "stage",
		HomeDir:  home,
		In:       input,
		Out:      &output,
		Resolver: testProjectResolver(t, `{"projects":[{"id":"virtual-1","type":"tidbx_virtual"}]}`),
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if strings.Contains(output.String(), "private-key") {
		t.Fatalf("configure output leaked private key:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "Default region code") {
		t.Fatalf("configure output missing default region prompt:\n%s", output.String())
	}
	if result.ProjectID != "virtual-1" || result.ProjectType != virtualProjectType || !result.CredentialsStored {
		t.Fatalf("unexpected configure result: %#v", result)
	}

	profile, err := config.Load(context.Background(), config.LoadOptions{
		Profile:         "stage",
		ProfileExplicit: true,
		HomeDir:         home,
	})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if profile.CloudProvider != "aws" || profile.RegionCode != "us-east-1" {
		t.Fatalf("unexpected profile: %#v", profile)
	}
	if profile.TiDBCloudPrivateKey != "private-key" {
		t.Fatal("private key was not stored")
	}
	if profile.ProjectID != "virtual-1" {
		t.Fatalf("project id was not stored: %#v", profile)
	}
}

func TestRunRejectsUnsupportedProviderRegion(t *testing.T) {
	input := strings.NewReader("ali-us-east-1\npublic-key\nprivate-key\n")

	_, err := Run(context.Background(), Options{
		HomeDir: t.TempDir(),
		In:      input,
		Out:     &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("expected invalid provider/region to fail")
	}
}

func TestRunNonInteractiveUsesEnvironment(t *testing.T) {
	home := t.TempDir()
	var output bytes.Buffer

	result, err := Run(context.Background(), Options{
		Profile:        "ci",
		HomeDir:        home,
		NonInteractive: true,
		Env: map[string]string{
			"TI_REGION_CODE":         "aws-us-east-1",
			"TIDB_CLOUD_PUBLIC_KEY":  "env-public",
			"TIDB_CLOUD_PRIVATE_KEY": "env-private",
		},
		Out:      &output,
		Resolver: testProjectResolver(t, `{"projects":[{"id":"virtual-env","type":"tidbx_virtual"}]}`),
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if strings.Contains(output.String(), "env-private") {
		t.Fatalf("configure output leaked private key:\n%s", output.String())
	}
	if result.Profile != "ci" || result.ProjectID != "virtual-env" {
		t.Fatalf("unexpected configure result: %#v", result)
	}

	profile, err := config.Load(context.Background(), config.LoadOptions{
		Profile:         "ci",
		ProfileExplicit: true,
		HomeDir:         home,
	})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if profile.CloudProvider != "aws" || profile.RegionCode != "us-east-1" {
		t.Fatalf("unexpected profile: %#v", profile)
	}
	if profile.TiDBCloudPublicKey != "env-public" || profile.TiDBCloudPrivateKey != "env-private" {
		t.Fatalf("env credentials not stored: %#v", profile)
	}
	if profile.ProjectID != "virtual-env" {
		t.Fatalf("env configure project id not stored: %#v", profile)
	}
}

func TestRunNonInteractiveRequiresMissingValues(t *testing.T) {
	_, err := Run(context.Background(), Options{
		HomeDir:        t.TempDir(),
		NonInteractive: true,
		Env: map[string]string{
			"TI_REGION_CODE":        "aws-us-east-1",
			"TIDB_CLOUD_PUBLIC_KEY": "env-public",
		},
		Out: &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("expected missing private key to fail")
	}
	if got := apperr.MessageFor(err); !strings.Contains(got, "non-interactive configure") {
		t.Fatalf("unexpected error: %q", got)
	}
}

func TestSelectVirtualProjectPaginatesAndIgnoresRegularProjects(t *testing.T) {
	lister := &scriptedProjectLister{responses: []apiiam.ListProjectsResponse{
		{Projects: []apiiam.Project{{ID: "regular-1", Type: "tidbx"}}, NextPageToken: "page-2"},
		{Projects: []apiiam.Project{{ID: "virtual-1", Type: virtualProjectType}}},
	}}
	project, err := selectVirtualProject(context.Background(), lister)
	if err != nil {
		t.Fatalf("select virtual project: %v", err)
	}
	if project.ID != "virtual-1" {
		t.Fatalf("unexpected project: %#v", project)
	}
	if len(lister.options) != 2 || lister.options[0].PageToken != "" || lister.options[1].PageToken != "page-2" {
		t.Fatalf("unexpected pagination: %#v", lister.options)
	}
}

func TestSelectVirtualProjectRejectsMissingAndMultipleMatches(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		_, err := selectVirtualProject(context.Background(), &scriptedProjectLister{responses: []apiiam.ListProjectsResponse{{
			Projects: []apiiam.Project{{ID: "regular-1", Type: "tidbx"}},
		}}})
		if apperr.CodeFor(err) != "config.virtual_project_not_found" {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("multiple", func(t *testing.T) {
		_, err := selectVirtualProject(context.Background(), &scriptedProjectLister{responses: []apiiam.ListProjectsResponse{{
			Projects: []apiiam.Project{{ID: "virtual-b", Type: virtualProjectType}, {ID: "virtual-a", Type: virtualProjectType}},
		}}})
		if apperr.CodeFor(err) != "config.virtual_project_ambiguous" || !strings.Contains(apperr.MessageFor(err), "virtual-a, virtual-b") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestSelectVirtualProjectRejectsRepeatedTokenAndMissingID(t *testing.T) {
	t.Run("repeated token", func(t *testing.T) {
		_, err := selectVirtualProject(context.Background(), &scriptedProjectLister{responses: []apiiam.ListProjectsResponse{
			{NextPageToken: "repeat"},
			{NextPageToken: "repeat"},
		}})
		if apperr.CodeFor(err) != "config.repeated_project_page_token" {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("missing id", func(t *testing.T) {
		_, err := selectVirtualProject(context.Background(), &scriptedProjectLister{responses: []apiiam.ListProjectsResponse{{
			Projects: []apiiam.Project{{Type: virtualProjectType}},
		}}})
		if apperr.CodeFor(err) != "config.invalid_virtual_project" {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestSelectVirtualProjectPropagatesCancellation(t *testing.T) {
	_, err := selectVirtualProject(context.Background(), &scriptedProjectLister{err: context.Canceled})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestRunDiscoveryFailurePreservesExistingProfile(t *testing.T) {
	home := t.TempDir()
	if err := store.WriteProfile(home, "stage", store.ConfigProfile{RegionCode: "aws-us-west-2", ProjectID: "virtual-old"}, store.CredentialsProfile{TiDBCloudPublicKey: "old-public", TiDBCloudPrivateKey: "old-private"}); err != nil {
		t.Fatal(err)
	}
	beforeConfig, err := os.ReadFile(store.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	beforeCredentials, err := os.ReadFile(store.CredentialsPath(home))
	if err != nil {
		t.Fatal(err)
	}

	_, err = Run(context.Background(), Options{
		Profile:             "stage",
		HomeDir:             home,
		NonInteractive:      true,
		RegionCode:          "aws-us-east-1",
		TiDBCloudPublicKey:  "new-public",
		TiDBCloudPrivateKey: "new-private",
		Resolver:            testProjectResolver(t, `{"projects":[{"id":"regular-1","type":"tidbx"}]}`),
	})
	if apperr.CodeFor(err) != "config.virtual_project_not_found" {
		t.Fatalf("unexpected error: %v", err)
	}
	afterConfig, _ := os.ReadFile(store.ConfigPath(home))
	afterCredentials, _ := os.ReadFile(store.CredentialsPath(home))
	if !bytes.Equal(beforeConfig, afterConfig) || !bytes.Equal(beforeCredentials, afterCredentials) {
		t.Fatalf("configure discovery failure changed profile files\nconfig before:\n%s\nconfig after:\n%s", beforeConfig, afterConfig)
	}
}

func TestRunReconfigureReplacesProjectID(t *testing.T) {
	home := t.TempDir()
	if err := store.WriteProfile(home, "stage", store.ConfigProfile{RegionCode: "aws-us-west-2", ProjectID: "virtual-old"}, store.CredentialsProfile{TiDBCloudPublicKey: "old-public", TiDBCloudPrivateKey: "old-private"}); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), Options{
		Profile:             "stage",
		HomeDir:             home,
		NonInteractive:      true,
		RegionCode:          "aws-us-east-1",
		TiDBCloudPublicKey:  "new-public",
		TiDBCloudPrivateKey: "new-private",
		Resolver:            testProjectResolver(t, `{"projects":[{"id":"virtual-new","type":"tidbx_virtual"}]}`),
	})
	if err != nil {
		t.Fatalf("reconfigure: %v", err)
	}
	profile, err := config.Load(context.Background(), config.LoadOptions{Profile: "stage", ProfileExplicit: true, HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	if profile.ProjectID != "virtual-new" || profile.TiDBCloudPublicKey != "new-public" {
		t.Fatalf("profile was not refreshed: %#v", profile)
	}
}

type scriptedProjectLister struct {
	responses []apiiam.ListProjectsResponse
	options   []apiiam.ListProjectsOptions
	err       error
}

func (l *scriptedProjectLister) ListProjects(_ context.Context, opts apiiam.ListProjectsOptions) (apiiam.ListProjectsResponse, error) {
	l.options = append(l.options, opts)
	if l.err != nil {
		return apiiam.ListProjectsResponse{}, l.err
	}
	if len(l.responses) == 0 {
		return apiiam.ListProjectsResponse{}, nil
	}
	response := l.responses[0]
	l.responses = l.responses[1:]
	return response, nil
}

func testProjectResolver(t *testing.T, response string) endpoints.Resolver {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta1/projects" {
			t.Fatalf("unexpected project path %s", r.URL.Path)
		}
		if r.URL.Query().Get("pageSize") != "100" {
			t.Fatalf("unexpected page size %q", r.URL.Query().Get("pageSize"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	return endpoints.Resolver{IAMBaseURL: server.URL}
}
