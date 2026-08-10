package db

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

func TestCreateBranch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters/cluster-1" {
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","servicePlan":"Starter"}`))
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta1/clusters/cluster-1/branches" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["displayName"] != "dev" {
			t.Fatalf("unexpected body: %#v", body)
		}
		_, _ = w.Write([]byte(`{"branchId":"branch-1","displayName":"dev","clusterId":"cluster-1","state":"CREATING"}`))
	}))
	defer server.Close()

	result, err := testService(server.URL).CreateBranch(context.Background(), CreateBranchOptions{
		Profile:     testProfile(),
		ClusterID:   "cluster-1",
		DisplayName: "dev",
	})
	if err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}
	if result.ID != "branch-1" || result.DisplayName != "dev" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestCreateBranchWaitsUntilActive(t *testing.T) {
	requests := make([]string, 0, 4)
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters/cluster-1":
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","servicePlan":"Starter"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1beta1/clusters/cluster-1/branches":
			_, _ = w.Write([]byte(`{"branchId":"branch-1","displayName":"dev","clusterId":"cluster-1","state":"CREATING"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters/cluster-1/branches/branch-1":
			gets++
			state := "CREATING"
			if gets == 2 {
				state = "ACTIVE"
			}
			_, _ = fmt.Fprintf(w, `{"branchId":"branch-1","displayName":"dev","clusterId":"cluster-1","state":%q}`, state)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	service := testService(server.URL)
	service.BranchWaitTimeout = time.Second
	service.BranchWaitPollInterval = time.Millisecond
	result, err := service.CreateBranch(context.Background(), CreateBranchOptions{
		Profile:         testProfile(),
		ClusterID:       "cluster-1",
		DisplayName:     "dev",
		WaitUntilActive: true,
	})
	if err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}
	if result.ID != "branch-1" || result.State != "ACTIVE" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if got := strings.Join(requests, ","); got != "GET /v1beta1/clusters/cluster-1,POST /v1beta1/clusters/cluster-1/branches,GET /v1beta1/clusters/cluster-1/branches/branch-1,GET /v1beta1/clusters/cluster-1/branches/branch-1" {
		t.Fatalf("unexpected requests %q", got)
	}
}

func TestCreateBranchWaitErrorsPreserveCreatedBranch(t *testing.T) {
	tests := []struct {
		name        string
		getResponse func(http.ResponseWriter)
		timeout     time.Duration
		wantCode    string
		wantText    string
	}{
		{
			name: "terminal state",
			getResponse: func(w http.ResponseWriter) {
				_, _ = w.Write([]byte(`{"branchId":"branch-1","displayName":"dev","clusterId":"cluster-1","state":"DELETED"}`))
			},
			timeout:  time.Second,
			wantCode: "db.branch_wait_terminal_state",
			wantText: "DELETED",
		},
		{
			name: "read failure",
			getResponse: func(w http.ResponseWriter) {
				http.Error(w, `{"message":"backend unavailable"}`, http.StatusInternalServerError)
			},
			timeout:  time.Second,
			wantCode: "db.branch_wait_read_failed",
			wantText: "could not read its state",
		},
		{
			name: "timeout",
			getResponse: func(w http.ResponseWriter) {
				_, _ = w.Write([]byte(`{"branchId":"branch-1","displayName":"dev","clusterId":"cluster-1","state":"CREATING"}`))
			},
			timeout:  10 * time.Millisecond,
			wantCode: "db.branch_wait_timeout",
			wantText: "was not deleted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters/cluster-1":
					_, _ = w.Write([]byte(`{"clusterId":"cluster-1","servicePlan":"Starter"}`))
				case r.Method == http.MethodPost && r.URL.Path == "/v1beta1/clusters/cluster-1/branches":
					_, _ = w.Write([]byte(`{"branchId":"branch-1","displayName":"dev","clusterId":"cluster-1","state":"CREATING"}`))
				case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters/cluster-1/branches/branch-1":
					tt.getResponse(w)
				default:
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
				}
			}))
			defer server.Close()

			service := testService(server.URL)
			service.BranchWaitTimeout = tt.timeout
			service.BranchWaitPollInterval = time.Millisecond
			_, err := service.CreateBranch(context.Background(), CreateBranchOptions{
				Profile:         testProfile(),
				ClusterID:       "cluster-1",
				DisplayName:     "dev",
				WaitUntilActive: true,
			})
			if apperr.CodeFor(err) != tt.wantCode {
				t.Fatalf("error code = %q, want %q: %v", apperr.CodeFor(err), tt.wantCode, err)
			}
			message := apperr.MessageFor(err)
			if !strings.Contains(message, "branch-1") || !strings.Contains(message, "cluster-1") || !strings.Contains(message, tt.wantText) {
				t.Fatalf("error should preserve branch identity and context, got %q", message)
			}
		})
	}
}

func TestListBranches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1beta1/clusters/cluster-1" {
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","servicePlan":"Starter"}`))
			return
		}
		if r.URL.Path != "/v1beta1/clusters/cluster-1/branches" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("pageSize") != "1" {
			t.Fatalf("unexpected query %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{
			"branches":[{"branchId":"branch-1","displayName":"dev","clusterId":"cluster-1","state":"ACTIVE"}],
			"nextPageToken":"token-2",
			"totalSize":1
		}`))
	}))
	defer server.Close()

	result, err := testService(server.URL).ListBranches(context.Background(), ListBranchesOptions{
		Profile:   testProfile(),
		ClusterID: "cluster-1",
		PageSize:  1,
	})
	if err != nil {
		t.Fatalf("ListBranches failed: %v", err)
	}
	if len(result.Branches) != 1 || result.Branches[0].ID != "branch-1" {
		t.Fatalf("unexpected branches: %#v", result.Branches)
	}
	if human := result.Human(); !strings.Contains(human, "dev") || !strings.Contains(human, "token-2") {
		t.Fatalf("unexpected text output:\n%s", human)
	}
}

func TestDescribeBranch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters/cluster-1" {
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","servicePlan":"Starter"}`))
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1beta1/clusters/cluster-1/branches/branch-1" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("view") != "FULL" {
			t.Fatalf("unexpected view %q", r.URL.Query().Get("view"))
		}
		_, _ = w.Write([]byte(`{"branchId":"branch-1","displayName":"dev","clusterId":"cluster-1","state":"ACTIVE"}`))
	}))
	defer server.Close()

	result, err := testService(server.URL).DescribeBranch(context.Background(), DescribeBranchOptions{
		Profile:   testProfile(),
		ClusterID: "cluster-1",
		BranchID:  "branch-1",
		View:      "FULL",
	})
	if err != nil {
		t.Fatalf("DescribeBranch failed: %v", err)
	}
	if result.ID != "branch-1" || result.State != "ACTIVE" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestDeleteBranch(t *testing.T) {
	requests := make([]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters/cluster-1":
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","servicePlan":"Starter"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters/cluster-1/branches/branch-1":
			_, _ = w.Write([]byte(`{"branchId":"branch-1","displayName":"dev","clusterId":"cluster-1","state":"ACTIVE"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1beta1/clusters/cluster-1/branches/branch-1":
			_, _ = w.Write([]byte(`{"branchId":"branch-1","displayName":"dev","clusterId":"cluster-1","state":"DELETED"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	result, err := testService(server.URL).DeleteBranch(context.Background(), DeleteBranchOptions{
		Profile:   testProfile(),
		ClusterID: "cluster-1",
		BranchID:  "branch-1",
	})
	if err != nil {
		t.Fatalf("DeleteBranch failed: %v", err)
	}
	if result.State != "DELETED" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if strings.Join(requests, ",") != "GET /v1beta1/clusters/cluster-1,GET /v1beta1/clusters/cluster-1/branches/branch-1,DELETE /v1beta1/clusters/cluster-1/branches/branch-1" {
		t.Fatalf("unexpected requests: %#v", requests)
	}
}

func TestBranchCommandsRejectNonStarterBeforeBranchRequest(t *testing.T) {
	tests := []struct {
		name string
		run  func(Service) error
	}{
		{name: "list", run: func(service Service) error {
			_, err := service.ListBranches(context.Background(), ListBranchesOptions{Profile: testProfile(), ClusterID: "cluster-1"})
			return err
		}},
		{name: "create", run: func(service Service) error {
			_, err := service.CreateBranch(context.Background(), CreateBranchOptions{Profile: testProfile(), ClusterID: "cluster-1", DisplayName: "dev"})
			return err
		}},
		{name: "describe", run: func(service Service) error {
			_, err := service.DescribeBranch(context.Background(), DescribeBranchOptions{Profile: testProfile(), ClusterID: "cluster-1", BranchID: "branch-1"})
			return err
		}},
		{name: "delete", run: func(service Service) error {
			_, err := service.DeleteBranch(context.Background(), DeleteBranchOptions{Profile: testProfile(), ClusterID: "cluster-1", BranchID: "branch-1"})
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/v1beta1/clusters/cluster-1" {
					t.Fatalf("branch endpoint was called before rejection: %s %s", r.Method, r.URL.Path)
				}
				_, _ = w.Write([]byte(`{"clusterId":"cluster-1","servicePlan":"Essential"}`))
			}))
			defer server.Close()

			err := tt.run(testService(server.URL))
			if apperr.CodeFor(err) != "db.not_starter_cluster" {
				t.Fatalf("unexpected error: %v", err)
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want only the cluster preflight", requests)
			}
		})
	}
}

func TestDryRunCreateBranchDoesNotSendRequest(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	result, err := testService(server.URL).DryRunCreateBranch(context.Background(), "ti db create-db-cluster-branch", CreateBranchOptions{
		Profile:         testProfile(),
		ClusterID:       "cluster-1",
		DisplayName:     "dev",
		WaitUntilActive: true,
	})
	if err != nil {
		t.Fatalf("DryRunCreateBranch failed: %v", err)
	}
	if !result.DryRun || result.Request.Method != http.MethodPost {
		t.Fatalf("unexpected dry-run: %#v", result)
	}
	if called {
		t.Fatal("dry-run should not send a request")
	}
	foundWait := false
	foundGuard := false
	for _, check := range result.Checks {
		if check.Name == "post_create_wait" && strings.Contains(check.Message, "5m0s") {
			foundWait = true
		}
		if check.Name == "starter_cluster_precondition" {
			foundGuard = true
		}
	}
	if !foundWait || !foundGuard {
		t.Fatalf("dry-run should describe the post-create wait and Starter precondition: %#v", result.Checks)
	}
}

func TestDryRunDeleteBranchDoesNotSendRequest(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	result, err := testService(server.URL).DryRunDeleteBranch(context.Background(), "ti db delete-db-cluster-branch", DeleteBranchOptions{
		Profile:   testProfile(),
		ClusterID: "cluster-1",
		BranchID:  "branch-1",
	})
	if err != nil {
		t.Fatalf("DryRunDeleteBranch failed: %v", err)
	}
	if !result.DryRun || result.Request.Method != http.MethodDelete {
		t.Fatalf("unexpected dry-run: %#v", result)
	}
	if called {
		t.Fatal("dry-run should not send a request")
	}
	for _, check := range result.Checks {
		if check.Name == "starter_cluster_precondition" {
			return
		}
	}
	t.Fatalf("dry-run should describe the Starter precondition: %#v", result.Checks)
}

func TestCreateBranchRequiresName(t *testing.T) {
	_, err := Service{}.DryRunCreateBranch(context.Background(), "ti db create-db-cluster-branch", CreateBranchOptions{
		Profile:   testProfile(),
		ClusterID: "cluster-1",
	})
	if err == nil {
		t.Fatal("expected missing branch name to fail")
	}
	if got := apperr.ExitCodeFor(err); got != 2 {
		t.Fatalf("expected exit code 2, got %d", got)
	}
}
