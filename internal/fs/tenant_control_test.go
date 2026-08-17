package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
)

func TestTenantControlCreateStoresOwnerTokenAndDoesNotInvokeCompanion(t *testing.T) {
	home := t.TempDir()
	profile := testProfile()
	displayName := "agent-workspace"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/admin/tenants" {
			t.Fatalf("request = %s %s", r.Method, r.URL.RequestURI())
		}
		assertAdminCredentialHeaders(t, r)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["display_name"] != displayName || body["public_key"] != nil || body["private_key"] != nil {
			t.Fatalf("body = %#v", body)
		}
		labels, ok := body["label"].(map[string]any)
		if !ok || labels["environment"] != "production" || labels["team"] != "ai" {
			t.Fatalf("labels = %#v", body["label"])
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tenant_id": "tenant-created", "display_name": displayName, "label": labels, "api_key": "owner-secret", "status": "provisioning",
			"cloud_provider": "aws", "region": "us-east-1",
		})
	}))
	defer server.Close()

	service := directTenantService(home, server.URL)
	service.CompanionPath = filepath.Join(t.TempDir(), "must-not-run")
	result, err := service.CreateFileSystem(context.Background(), CreateFileSystemOptions{
		Profile: profile, DisplayName: &displayName, Labels: map[string]string{"environment": "production", "team": "ai"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FileSystemID != "tenant-created" || result.DisplayName != displayName || result.Labels["team"] != "ai" || result.FSToken != "owner-secret" || !result.CredentialsStored {
		t.Fatalf("result = %#v", result)
	}
	credential, err := fscred.GetCredential(home, profile.Name, result.FileSystemID)
	if err != nil {
		t.Fatal(err)
	}
	if credential.APIKey != "owner-secret" || credential.RegionCode != "aws-us-east-1" {
		t.Fatalf("credential = %#v", credential)
	}
}

func TestTenantControlCreateWithoutMetadataUsesServerFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 0 {
			t.Fatalf("body = %#v", body)
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"tenant_id": "tenant-fallback", "api_key": "owner-secret", "status": "active"})
	}))
	defer server.Close()

	result, err := directTenantService(t.TempDir(), server.URL).CreateFileSystem(context.Background(), CreateFileSystemOptions{Profile: testProfile()})
	if err != nil {
		t.Fatal(err)
	}
	if result.DisplayName != "tenant-fallback" || result.Labels == nil || len(result.Labels) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestTenantControlCreateWaitUsesOnlyCompanionReadiness(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	t.Setenv("TI_FAKE_DRIVE9_STAT_FAILURE_SEQUENCE", filepath.Join(t.TempDir(), "stat-attempted"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"tenant_id": "tenant-1", "display_name": "tenant-1", "label": map[string]string{}, "api_key": "fs-secret", "status": "provisioning"})
	}))
	defer server.Close()

	service := directTenantService(home, server.URL)
	service.CompanionPath = companion
	service.FSReadyWaitTimeout = time.Second
	service.FSReadyWaitPollInterval = time.Millisecond
	result, err := service.CreateFileSystem(context.Background(), CreateFileSystemOptions{Profile: testProfile(), WaitUntilReady: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ready" {
		t.Fatalf("result = %#v", result)
	}
	statCalls := 0
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if len(call.Args) > 0 && call.Args[0] == "create" {
			t.Fatalf("direct create invoked companion create: %#v", call.Args)
		}
		if hasArgPrefix(call.Args, []string{"fs", "stat"}) {
			statCalls++
		}
	}
	if statCalls != 2 {
		t.Fatalf("readiness calls = %d, want 2", statCalls)
	}
}

func TestTenantControlListFiltersPaginationMetadataAndLocalTokenJoin(t *testing.T) {
	home := t.TempDir()
	profile := testProfile()
	if _, err := fscred.StoreCredential(home, profile, "tenant-2", "aws-us-east-1", fsTestToken(t, "tenant-2"), false); err != nil {
		t.Fatal(err)
	}
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		assertAdminCredentialHeaders(t, r)
		query := r.URL.Query()
		if query.Get("page_size") != "100" || query.Get("display_name") != "workspace" || query.Get("label") != "environment==production" {
			t.Fatalf("query = %q", r.URL.RawQuery)
		}
		switch query.Get("page") {
		case "1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tenants": []map[string]any{{"tenant_id": "tenant-2", "display_name": "workspace-two", "label": map[string]string{"environment": "production"}, "status": "active", "kind": "live", "quota": tenantQuotaFixture()}},
				"page":    1, "page_size": 100, "next_page": 2,
			})
		case "2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tenants": []map[string]any{{"tenant_id": "tenant-1", "display_name": "", "label": nil, "status": "active", "kind": "live", "quota": tenantQuotaFixture()}},
				"page":    2, "page_size": 100,
			})
		default:
			t.Fatalf("unexpected page %q", query.Get("page"))
		}
	}))
	defer server.Close()

	displayName := "workspace"
	result, err := directTenantService(home, server.URL).ListFileSystems(context.Background(), ListFileSystemsOptions{
		Profile: profile, DisplayName: &displayName, Label: &LabelFilter{Key: "environment", Value: "production"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 || len(result.FileSystems) != 2 || result.FileSystems[0].FileSystemID != "tenant-1" || result.FileSystems[1].FileSystemID != "tenant-2" {
		t.Fatalf("result = %#v, pages = %d", result, pages)
	}
	if result.FileSystems[0].DisplayName != "tenant-1" || result.FileSystems[0].Labels == nil || result.FileSystems[0].HasLocalToken {
		t.Fatalf("fallback item = %#v", result.FileSystems[0])
	}
	if !result.FileSystems[1].HasLocalToken || result.FileSystems[1].Quota == nil {
		t.Fatalf("local item = %#v", result.FileSystems[1])
	}
	text := result.Human()
	if !strings.Contains(text, "DISPLAY_NAME") || !strings.Contains(text, "workspace-two") {
		t.Fatalf("text output = %q", text)
	}
}

func TestTenantControlDescribeAndDeleteUseIDs(t *testing.T) {
	home := t.TempDir()
	profile := testProfile()
	for _, id := range []string{"tenant-1", "tenant-2"} {
		if _, err := fscred.StoreCredential(home, profile, id, "aws-us-east-1", fsTestToken(t, id), false); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertAdminCredentialHeaders(t, r)
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"tenant_id": "tenant-1", "display_name": "workspace-one", "label": map[string]string{"team": "ai"}, "status": "active", "kind": "live", "quota": tenantQuotaFixture()})
		case http.MethodDelete:
			if r.ContentLength > 0 {
				t.Fatalf("delete unexpectedly had a body")
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"tenant_id": "tenant-1", "status": "deleting"})
		default:
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	service := directTenantService(home, server.URL)
	described, err := service.DescribeFileSystem(context.Background(), profile, "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if described.DisplayName != "workspace-one" || described.Labels["team"] != "ai" || !described.HasLocalToken || !strings.Contains(described.Human(), "Labels: team=ai") {
		t.Fatalf("describe = %#v", described)
	}
	deleted, err := service.DeleteFileSystem(context.Background(), DeleteFileSystemOptions{Profile: profile, FileSystemID: "tenant-1"})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Status != "deleting" || !deleted.CredentialsRemoved {
		t.Fatalf("delete = %#v", deleted)
	}
	if _, err := fscred.GetCredential(home, profile.Name, "tenant-1"); apperr.CodeFor(err) != "fs.credential_not_found" {
		t.Fatalf("selected credential remains: %v", err)
	}
	if _, err := fscred.GetCredential(home, profile.Name, "tenant-2"); err != nil {
		t.Fatalf("unrelated credential was removed: %v", err)
	}
}

func TestTenantControlValidationRejectsInvalidMetadataBeforeNetwork(t *testing.T) {
	validDisplay, labels, err := ParseTenantMetadata("agent-workspace", true, []string{"environment=production", "example.com/empty="})
	if err != nil || validDisplay == nil || labels["example.com/empty"] != "" {
		t.Fatalf("valid metadata = %v %#v %#v", err, validDisplay, labels)
	}
	tests := []struct {
		name string
		run  func() error
		code string
	}{
		{name: "explicit empty display name", code: "fs.invalid_display_name", run: func() error { _, _, err := ParseTenantMetadata("", true, nil); return err }},
		{name: "surrounding whitespace", code: "fs.invalid_display_name", run: func() error { _, _, err := ParseTenantMetadata(" agent-workspace ", true, nil); return err }},
		{name: "duplicate label", code: "fs.invalid_label", run: func() error {
			_, _, err := ParseTenantMetadata("", false, []string{"team=ai", "team=data"})
			return err
		}},
		{name: "invalid label key", code: "fs.invalid_label", run: func() error { _, _, err := ParseTenantMetadata("", false, []string{"Example.com/team=ai"}); return err }},
		{name: "oversized label prefix segment", code: "fs.invalid_label", run: func() error {
			_, _, err := ParseTenantMetadata("", false, []string{strings.Repeat("a", 64) + ".example/team=ai"})
			return err
		}},
		{name: "invalid label value", code: "fs.invalid_label", run: func() error { _, _, err := ParseTenantMetadata("", false, []string{"team=-ai"}); return err }},
		{name: "missing label separator", code: "fs.invalid_label", run: func() error { _, _, err := ParseTenantMetadata("", false, []string{"team"}); return err }},
		{name: "wildcard display filter", code: "fs.invalid_display_name", run: func() error { _, _, err := ParseTenantListFilters("work%", true, nil); return err }},
		{name: "multiple list labels", code: "fs.invalid_label", run: func() error {
			_, _, err := ParseTenantListFilters("", false, []string{"team=ai", "env=prod"})
			return err
		}},
	}
	tooMany := make([]string, maxTenantLabels+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("key%d=value", i)
	}
	tests = append(tests, struct {
		name string
		run  func() error
		code string
	}{name: "too many labels", code: "fs.invalid_label", run: func() error { _, _, err := ParseTenantMetadata("", false, tooMany); return err }})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); apperr.CodeFor(err) != test.code {
				t.Fatalf("error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestTenantControlMapsErrorsAndRejectsContractViolations(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		run        func(Service) error
		wantCode   string
	}{
		{name: "display conflict", statusCode: http.StatusConflict, body: `{"error":"conflict"}`, wantCode: "fs.display_name_conflict", run: func(s Service) error {
			name := "agent-workspace"
			_, err := s.CreateFileSystem(context.Background(), CreateFileSystemOptions{Profile: testProfile(), DisplayName: &name})
			return err
		}},
		{name: "control plane unavailable", statusCode: http.StatusNotFound, body: `{"error":"admin tenant API not enabled"}`, wantCode: "fs.control_plane_unavailable", run: func(s Service) error {
			_, err := s.ListFileSystems(context.Background(), ListFileSystemsOptions{Profile: testProfile()})
			return err
		}},
		{name: "item missing", statusCode: http.StatusNotFound, body: `{"error":"tenant not found"}`, wantCode: "fs.resource_not_found", run: func(s Service) error {
			_, err := s.DescribeFileSystem(context.Background(), testProfile(), "tenant-missing")
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.statusCode)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			if err := test.run(directTenantService(t.TempDir(), server.URL)); apperr.CodeFor(err) != test.wantCode {
				t.Fatalf("error = %v, want %s", err, test.wantCode)
			}
		})
	}

	mismatch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tenant_id": "tenant-other", "display_name": "tenant-other", "label": map[string]string{}, "status": "active"})
	}))
	defer mismatch.Close()
	if _, err := directTenantService(t.TempDir(), mismatch.URL).DescribeFileSystem(context.Background(), testProfile(), "tenant-1"); apperr.CodeFor(err) != "fs.api_contract" {
		t.Fatalf("mismatch error = %v", err)
	}

	missingToken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tenant_id": "tenant-1", "display_name": "tenant-1", "label": map[string]string{}, "status": "active"})
	}))
	defer missingToken.Close()
	if _, err := directTenantService(t.TempDir(), missingToken.URL).CreateFileSystem(context.Background(), CreateFileSystemOptions{Profile: testProfile()}); apperr.CodeFor(err) != "fs.api_contract" {
		t.Fatalf("missing token error = %v", err)
	}
}

func TestTenantControlDeleteFailurePreservesCredential(t *testing.T) {
	home := t.TempDir()
	profile := testProfile()
	if _, err := fscred.StoreCredential(home, profile, "tenant-1", "aws-us-east-1", fsTestToken(t, "tenant-1"), false); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"temporary backend failure"}`))
	}))
	defer server.Close()
	if _, err := directTenantService(home, server.URL).DeleteFileSystem(context.Background(), DeleteFileSystemOptions{Profile: profile, FileSystemID: "tenant-1"}); err == nil {
		t.Fatal("delete should fail")
	}
	if _, err := fscred.GetCredential(home, profile.Name, "tenant-1"); err != nil {
		t.Fatalf("failed delete removed credentials: %v", err)
	}
}

func directTenantService(homeDir, baseURL string) Service {
	return Service{HomeDir: homeDir, Resolver: supportedFSManifestResolver(baseURL), CompanionPath: filepath.Join(homeDir, "companion-must-not-run")}
}

func assertAdminCredentialHeaders(t *testing.T, request *http.Request) {
	t.Helper()
	if request.Header.Get("X-TiDBCloud-Public-Key") != "public" || request.Header.Get("X-TiDBCloud-Private-Key") != "private" {
		t.Fatalf("credential headers = %q/%q", request.Header.Get("X-TiDBCloud-Public-Key"), request.Header.Get("X-TiDBCloud-Private-Key"))
	}
}

func tenantQuotaFixture() map[string]any {
	return map[string]any{
		"config": map[string]any{"max_storage_size": 1024, "max_file_size": 128, "max_file_count": 1000, "tidbcloud_spending_limit": nil},
		"usage":  map[string]any{"storage_bytes": 12, "reserved_bytes": 3, "file_count": 2},
	}
}

func TestTenantControlCreatePersistenceFailureReturnsRecoveryData(t *testing.T) {
	home := t.TempDir()
	credentialRoot := filepath.Join(home, ".ti", "fs_credentials")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := os.RemoveAll(credentialRoot); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(credentialRoot, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"tenant_id": "tenant-1", "display_name": "tenant-1", "label": map[string]string{}, "api_key": "one-time-owner-token", "status": "active"})
	}))
	defer server.Close()
	var stderr strings.Builder
	service := directTenantService(home, server.URL)
	service.Stderr = &stderr
	result, err := service.CreateFileSystem(context.Background(), CreateFileSystemOptions{Profile: testProfile()})
	if err != nil {
		t.Fatal(err)
	}
	if result.FSToken != "one-time-owner-token" || result.CredentialsStored || !strings.Contains(stderr.String(), "was created") || strings.Contains(stderr.String(), result.FSToken) {
		t.Fatalf("result = %#v stderr = %q", result, stderr.String())
	}
}

func TestTenantControlCreatePreflightFailsBeforeRemoteMutation(t *testing.T) {
	home := t.TempDir()
	credentialRoot := filepath.Join(home, ".ti", "fs_credentials")
	if err := os.MkdirAll(filepath.Dir(credentialRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	_, err := directTenantService(home, server.URL).CreateFileSystem(context.Background(), CreateFileSystemOptions{Profile: testProfile()})
	if apperr.CodeFor(err) != "fs.credential_store_preflight" {
		t.Fatalf("error = %v, want fs.credential_store_preflight", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("credential preflight failure sent %d remote requests", requests.Load())
	}
}

func TestTenantControlCreateWaitTimeoutPreservesCredential(t *testing.T) {
	home := t.TempDir()
	companion, _ := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_STAT_ALWAYS_FAIL", "1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tenant_id": "tenant-timeout", "display_name": "tenant-timeout", "label": map[string]string{}, "api_key": "owner-secret", "status": "provisioning",
		})
	}))
	defer server.Close()

	service := directTenantService(home, server.URL)
	service.CompanionPath = companion
	service.FSReadyWaitTimeout = 10 * time.Millisecond
	service.FSReadyWaitPollInterval = time.Millisecond
	_, err := service.CreateFileSystem(context.Background(), CreateFileSystemOptions{Profile: testProfile(), WaitUntilReady: true})
	if apperr.CodeFor(err) != "fs.ready_wait_timeout" || !strings.Contains(apperr.MessageFor(err), "tenant-timeout") {
		t.Fatalf("error = %v, want timeout retaining the created ID", err)
	}
	credential, getErr := fscred.GetCredential(home, testProfile().Name, "tenant-timeout")
	if getErr != nil || credential.APIKey != "owner-secret" {
		t.Fatalf("readiness timeout removed credential: credential=%#v err=%v", credential, getErr)
	}
}

func TestTenantControlMissingCredentialsFailBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	profile := testProfile()
	profile.TiDBCloudPublicKey = ""
	profile.TiDBCloudPrivateKey = ""
	service := directTenantService(t.TempDir(), server.URL)
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "create", run: func() error {
			_, err := service.CreateFileSystem(context.Background(), CreateFileSystemOptions{Profile: profile})
			return err
		}},
		{name: "list", run: func() error {
			_, err := service.ListFileSystems(context.Background(), ListFileSystemsOptions{Profile: profile})
			return err
		}},
		{name: "describe", run: func() error {
			_, err := service.DescribeFileSystem(context.Background(), profile, "tenant-1")
			return err
		}},
		{name: "delete", run: func() error {
			_, err := service.DeleteFileSystem(context.Background(), DeleteFileSystemOptions{Profile: profile, FileSystemID: "tenant-1"})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); apperr.CodeFor(err) != "auth.missing_credentials" {
				t.Fatalf("error = %v, want auth.missing_credentials", err)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("missing credentials sent %d remote requests", requests.Load())
	}
}

func TestTenantControlRejectsInvalidPagination(t *testing.T) {
	tests := []struct {
		name    string
		respond func(page string) map[string]any
	}{
		{name: "mismatched page", respond: func(string) map[string]any {
			return map[string]any{"tenants": []any{}, "page": 2, "page_size": 100}
		}},
		{name: "regressing next page", respond: func(string) map[string]any {
			return map[string]any{"tenants": []any{}, "page": 1, "page_size": 100, "next_page": 1}
		}},
		{name: "duplicate ID", respond: func(page string) map[string]any {
			result := map[string]any{"tenants": []map[string]any{{"tenant_id": "tenant-1", "display_name": "tenant-1", "label": map[string]string{}}}, "page_size": 100}
			if page == "1" {
				result["page"] = 1
				result["next_page"] = 2
			} else {
				result["page"] = 2
			}
			return result
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(test.respond(r.URL.Query().Get("page")))
			}))
			defer server.Close()
			_, err := directTenantService(t.TempDir(), server.URL).ListFileSystems(context.Background(), ListFileSystemsOptions{Profile: testProfile()})
			if apperr.CodeFor(err) != "fs.api_contract" {
				t.Fatalf("error = %v, want fs.api_contract", err)
			}
		})
	}
}

func TestTenantControlRemoteErrorsRetainSafeDetails(t *testing.T) {
	t.Run("forbidden detail", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"organization policy denied tenant inventory"}`))
		}))
		defer server.Close()
		_, err := directTenantService(t.TempDir(), server.URL).ListFileSystems(context.Background(), ListFileSystemsOptions{Profile: testProfile()})
		if apperr.CodeFor(err) != "authz.permission_denied" || !strings.Contains(apperr.MessageFor(err), "organization policy denied tenant inventory") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("server request ID", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Request-ID", "request-tenant-500")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"temporary failure"}`))
		}))
		defer server.Close()
		_, err := directTenantService(t.TempDir(), server.URL).ListFileSystems(context.Background(), ListFileSystemsOptions{Profile: testProfile()})
		if apperr.CodeFor(err) != "api.remote_error" || !strings.Contains(apperr.MessageFor(err), "request-tenant-500") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("create is not retried", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()
		_, err := directTenantService(t.TempDir(), server.URL).CreateFileSystem(context.Background(), CreateFileSystemOptions{Profile: testProfile()})
		if err == nil || requests.Load() != 1 {
			t.Fatalf("error = %v, requests = %d", err, requests.Load())
		}
	})
}

func TestTenantControlMigratesLegacyCredentialForDescribeAndDelete(t *testing.T) {
	home := t.TempDir()
	profile := testProfile()
	if err := fscred.Store(home, profile, "workspace", "tenant-legacy", "aws", "aws-us-east-1", fsTestToken(t, "tenant-legacy")); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"tenant_id": "tenant-legacy", "display_name": "legacy-workspace", "label": map[string]string{}, "status": "active"})
		case http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"tenant_id": "tenant-legacy", "status": "deleting"})
		}
	}))
	defer server.Close()
	service := directTenantService(home, server.URL)
	described, err := service.DescribeFileSystem(context.Background(), profile, "tenant-legacy")
	if err != nil || !described.HasLocalToken {
		t.Fatalf("describe = %#v err=%v", described, err)
	}
	deleted, err := service.DeleteFileSystem(context.Background(), DeleteFileSystemOptions{Profile: profile, FileSystemID: "tenant-legacy"})
	if err != nil || !deleted.CredentialsRemoved {
		t.Fatalf("delete = %#v err=%v", deleted, err)
	}
	if _, err := fscred.GetCredential(home, profile.Name, "tenant-legacy"); apperr.CodeFor(err) != "fs.credential_not_found" {
		t.Fatalf("migrated credential remains: %v", err)
	}
}
