package organization

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
)

func TestListProjects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("pageSize"); got != "1" {
			t.Fatalf("unexpected pageSize %q", got)
		}
		if got := r.URL.Query().Get("pageToken"); got != "token-1" {
			t.Fatalf("unexpected pageToken %q", got)
		}
		_, _ = w.Write([]byte(`{
			"projects": [
				{
					"id": "project-1",
					"name": "Project 1",
					"type": "tidbx_virtual",
					"org_id": "org-1",
					"cluster_count": 2,
					"user_count": 3,
					"create_timestamp": "1688460316",
					"aws_cmek_enabled": false
				}
			],
			"nextPageToken": "token-2"
		}`))
	}))
	defer server.Close()

	service := Service{
		Resolver: endpoints.Resolver{IAMBaseURL: server.URL},
	}
	result, err := service.ListProjects(context.Background(), ListProjectsOptions{
		Profile:   testProfile(),
		PageSize:  1,
		PageToken: "token-1",
	})
	if err != nil {
		t.Fatalf("ListProjects failed: %v", err)
	}
	if len(result.Projects) != 1 || result.Projects[0].ID != "project-1" {
		t.Fatalf("unexpected projects: %#v", result.Projects)
	}
	if result.Projects[0].Type != "tidbx_virtual" {
		t.Fatalf("project type = %q, want tidbx_virtual", result.Projects[0].Type)
	}
	if result.NextPageToken != "token-2" {
		t.Fatalf("unexpected next page token %q", result.NextPageToken)
	}
	if human := result.Human(); !strings.Contains(human, "TYPE") || !strings.Contains(human, "tidbx_virtual") || !strings.Contains(human, "next_page_token") {
		t.Fatalf("unexpected text output:\n%s", human)
	}
}

func TestListProjectsValidatesPageSize(t *testing.T) {
	_, err := Service{}.ListProjects(context.Background(), ListProjectsOptions{
		Profile:  testProfile(),
		PageSize: -1,
	})
	if err == nil {
		t.Fatal("expected negative page size to fail")
	}
	if got := apperr.ExitCodeFor(err); got != 2 {
		t.Fatalf("expected exit code 2, got %d", got)
	}
}

func TestListProjectsAuthAndPermissionErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		exitCode   int
	}{
		{name: "unauthenticated", statusCode: http.StatusUnauthorized, exitCode: 3},
		{name: "permission denied", statusCode: http.StatusForbidden, exitCode: 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(`{"message":"denied"}`))
			}))
			defer server.Close()

			_, err := Service{
				Resolver: endpoints.Resolver{IAMBaseURL: server.URL},
			}.ListProjects(context.Background(), ListProjectsOptions{Profile: testProfile()})
			if err == nil {
				t.Fatal("expected ListProjects to fail")
			}
			if got := apperr.ExitCodeFor(err); got != tt.exitCode {
				t.Fatalf("expected exit code %d, got %d", tt.exitCode, got)
			}
		})
	}
}

func testProfile() *config.Profile {
	return &config.Profile{
		Name:                "test",
		CloudProvider:       "aws",
		RegionCode:          "us-east-1",
		TiDBCloudPublicKey:  "public",
		TiDBCloudPrivateKey: "private",
	}
}
