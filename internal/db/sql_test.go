package db

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/db/connectionstring"
	"github.com/tidbcloud/ti-cli/internal/db/sqlcred"
)

func TestPrepareQueryAccessCreatesAndStoresCredentials(t *testing.T) {
	var created []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters/cluster-1":
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo","clusterPlan":"STARTER","endpoints":{"public":{"host":"gateway.example.com","port":4000}}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters/cluster-1/sqlUsers":
			_, _ = w.Write([]byte(`{"sqlUsers":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1beta1/clusters/cluster-1/sqlUsers":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			created = append(created, body)
			_, _ = w.Write([]byte(`{"userName":"prefix.` + body["userName"].(string) + `","authMethod":"mysql_native_password","builtinRole":"` + body["builtinRole"].(string) + `"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	result, err := testSQLService(server.URL, home).PrepareQueryAccess(context.Background(), PrepareQueryAccessOptions{
		Profile:   testProfile(),
		ClusterID: "cluster-1",
	})
	if err != nil {
		t.Fatalf("PrepareQueryAccess failed: %v", err)
	}
	if len(created) != 3 {
		t.Fatalf("expected 3 SQL users to be created, got %#v", created)
	}
	if result.Users[string(sqlcred.ReadWrite)].Status != "created" {
		t.Fatalf("unexpected result: %#v", result)
	}
	path, err := sqlcred.CredentialsPath(home, "cluster-1")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	if !strings.Contains(string(data), "[read_only]") || !strings.Contains(string(data), "prefix.ti_admin") {
		t.Fatalf("unexpected credentials:\n%s", string(data))
	}
	if strings.Contains(string(data), ".ti/credentials") {
		t.Fatalf("DB credentials should not be stored in main credentials shape:\n%s", string(data))
	}
}

func TestPrepareQueryAccessRejectsNonStarterBeforeIAMOrCredentialWrite(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/v1beta1/clusters/cluster-1" {
			t.Fatalf("IAM request was sent before rejection: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"clusterId":"cluster-1","servicePlan":"Essential"}`))
	}))
	defer server.Close()

	home := t.TempDir()
	_, err := testSQLService(server.URL, home).PrepareQueryAccess(context.Background(), PrepareQueryAccessOptions{
		Profile: testProfile(), ClusterID: "cluster-1",
	})
	if apperr.CodeFor(err) != "db.not_starter_cluster" {
		t.Fatalf("unexpected error: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want only the cluster preflight", requests)
	}
	if _, err := os.Stat(filepath.Join(home, ".ti", "db_users")); !os.IsNotExist(err) {
		t.Fatalf("non-Starter rejection wrote local DB credentials: %v", err)
	}
}

func TestDryRunPrepareQueryAccessDescribesStarterPrecondition(t *testing.T) {
	result, err := Service{}.DryRunPrepareQueryAccess(context.Background(), "ti db create-db-sql-users", PrepareQueryAccessOptions{
		Profile: testProfile(), ClusterID: "cluster-1",
	})
	if err != nil {
		t.Fatalf("DryRunPrepareQueryAccess failed: %v", err)
	}
	for _, check := range result.Checks {
		if check.Name == "starter_cluster_precondition" {
			return
		}
	}
	t.Fatalf("dry-run should describe the Starter precondition: %#v", result.Checks)
}

func TestCreateConnectionString(t *testing.T) {
	home := t.TempDir()
	writeSQLCreds(t, home, "cluster-1")
	server := clusterEndpointServer(t)
	defer server.Close()

	result, err := testSQLService(server.URL, home).CreateConnectionString(context.Background(), CreateConnectionStringOptions{
		Profile:   testProfile(),
		ClusterID: "cluster-1",
		Database:  "app",
		Format:    connectionstring.FormatJDBC,
		ReadOnly:  true,
	})
	if err != nil {
		t.Fatalf("CreateConnectionString failed: %v", err)
	}
	if result.AccessMode != sqlcred.ReadOnly || result.Username != "prefix.ti_ro" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if !strings.Contains(result.ConnectionString, "jdbc:mysql://gateway.example.com:4000/app?") ||
		!strings.Contains(result.ConnectionString, "user=prefix.ti_ro") {
		t.Fatalf("unexpected connection string: %s", result.ConnectionString)
	}
}

func TestExecuteSQLHTTP(t *testing.T) {
	home := t.TempDir()
	writeSQLCreds(t, home, "cluster-1")
	clusterServer := clusterEndpointServer(t)
	defer clusterServer.Close()
	var sqlBody map[string]string
	sqlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "prefix.ti_rw" || password != "rw-pass" {
			t.Fatalf("unexpected basic auth user=%q password=%q ok=%t", user, password, ok)
		}
		if err := json.NewDecoder(r.Body).Decode(&sqlBody); err != nil {
			t.Fatalf("decode SQL body: %v", err)
		}
		_, _ = w.Write([]byte(`{"types":[{"name":"n","type":"INT","nullable":false}],"rows":[["1"]]}`))
	}))
	defer sqlServer.Close()

	result, err := testSQLService(clusterServer.URL, home).withSQLHTTPBaseURL(sqlServer.URL).ExecuteSQL(context.Background(), ExecuteSQLOptions{
		Profile:   testProfile(),
		ClusterID: "cluster-1",
		SQL:       "select 1",
	})
	if err != nil {
		t.Fatalf("ExecuteSQL failed: %v", err)
	}
	if sqlBody["query"] != "select 1" {
		t.Fatalf("unexpected SQL body: %#v", sqlBody)
	}
	if result.Transport != "https" || result.AccessMode != sqlcred.ReadWrite || result.RowCount != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestConnectionAndSQLRejectNonStarterBeforeCredentialOrSQLAccess(t *testing.T) {
	clusterRequests := 0
	clusterServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clusterRequests++
		if r.Method != http.MethodGet || r.URL.Path != "/v1beta1/clusters/cluster-1" {
			t.Fatalf("unexpected cluster request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"clusterId":"cluster-1","servicePlan":"Essential","endpoints":{"public":{"host":"gateway.example.com","port":4000}}}`))
	}))
	defer clusterServer.Close()

	sqlRequests := 0
	sqlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sqlRequests++
		t.Fatalf("SQL request was sent after non-Starter rejection: %s %s", r.Method, r.URL.Path)
	}))
	defer sqlServer.Close()

	home := t.TempDir()
	service := testSQLService(clusterServer.URL, home).withSQLHTTPBaseURL(sqlServer.URL)
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "connection string", run: func() error {
			_, err := service.CreateConnectionString(context.Background(), CreateConnectionStringOptions{Profile: testProfile(), ClusterID: "cluster-1"})
			return err
		}},
		{name: "SQL execution", run: func() error {
			_, err := service.ExecuteSQL(context.Background(), ExecuteSQLOptions{Profile: testProfile(), ClusterID: "cluster-1", SQL: "select 1"})
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if apperr.CodeFor(err) != "db.not_starter_cluster" {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
	if clusterRequests != len(tests) {
		t.Fatalf("cluster requests = %d, want %d", clusterRequests, len(tests))
	}
	if sqlRequests != 0 {
		t.Fatalf("SQL requests = %d, want 0", sqlRequests)
	}
	if _, err := os.Stat(filepath.Join(home, ".ti", "db_users")); !os.IsNotExist(err) {
		t.Fatalf("non-Starter connection path accessed local DB credentials: %v", err)
	}
}

func TestConnectionRequiresPreparedCredentials(t *testing.T) {
	server := clusterEndpointServer(t)
	defer server.Close()
	_, err := testSQLService(server.URL, t.TempDir()).CreateConnectionString(context.Background(), CreateConnectionStringOptions{
		Profile:   testProfile(),
		ClusterID: "cluster-1",
	})
	if err == nil {
		t.Fatal("expected missing credentials")
	}
	if got := apperr.MessageFor(err); !strings.Contains(got, "create-db-sql-users") {
		t.Fatalf("unexpected error: %q", got)
	}
}

func TestAccessModeFlagsAreMutuallyExclusive(t *testing.T) {
	_, err := accessMode(true, true, false)
	if err == nil {
		t.Fatal("expected conflict")
	}
	if got := apperr.ExitCodeFor(err); got != 2 {
		t.Fatalf("expected exit code 2, got %d", got)
	}
}

func testSQLService(baseURL, home string) Service {
	return Service{
		Resolver: endpoints.Resolver{
			StarterBaseURL: baseURL,
			IAMBaseURL:     baseURL,
		},
		HomeDir: home,
	}
}

func (s Service) withSQLHTTPBaseURL(baseURL string) Service {
	s.SQLHTTPBaseURL = baseURL
	return s
}

func clusterEndpointServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1beta1/clusters/cluster-1" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo","clusterPlan":"STARTER","endpoints":{"public":{"host":"gateway.example.com","port":4000}}}`))
	}))
}

func writeSQLCreds(t *testing.T, home, clusterID string) {
	t.Helper()
	if err := sqlcred.Write(home, clusterID, sqlcred.Document{
		ReadOnly:  sqlcred.Credential{Username: "prefix.ti_ro", Password: "ro-pass"},
		ReadWrite: sqlcred.Credential{Username: "prefix.ti_rw", Password: "rw-pass"},
		Admin:     sqlcred.Credential{Username: "prefix.ti_admin", Password: "admin-pass"},
	}); err != nil {
		t.Fatalf("write SQL credentials: %v", err)
	}
	path, err := sqlcred.CredentialsPath(home, clusterID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, filepath.Join(".ti", "db_users", clusterID, "credentials")) {
		t.Fatalf("unexpected SQL credentials path: %s", path)
	}
}
