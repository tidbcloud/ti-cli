package telemetry

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/tidbcloud/ti-cli/internal/config/store"
	"github.com/tidbcloud/ti-cli/internal/settings"
	"github.com/tidbcloud/ti-cli/internal/version"
)

func TestStartResolutionAndStateCreation(t *testing.T) {
	tests := []struct {
		name        string
		info        version.Info
		env         map[string]string
		preferences string
		wantSession bool
	}{
		{name: "release default", info: releaseInfo(), wantSession: true},
		{name: "development default", info: version.Info{Version: "dev"}, wantSession: false},
		{name: "local build default", info: version.Info{Version: "0.2.0-dev", InstallSource: "local"}, wantSession: false},
		{name: "CI default", info: releaseInfo(), env: map[string]string{"CI": "true"}, wantSession: false},
		{name: "persistent disable", info: releaseInfo(), preferences: "[telemetry]\nenabled = false\n", wantSession: false},
		{name: "environment enable overrides development", info: version.Info{Version: "dev"}, env: map[string]string{EnvironmentVariable: "on"}, wantSession: true},
		{name: "environment enable overrides persistent disable", info: releaseInfo(), env: map[string]string{EnvironmentVariable: "true"}, preferences: "[telemetry]\nenabled = false\n", wantSession: true},
		{name: "persistent enable overrides local default", info: version.Info{Version: "0.2.0-dev", InstallSource: "local"}, preferences: "[telemetry]\nenabled = true\n", wantSession: true},
		{name: "environment disable", info: releaseInfo(), env: map[string]string{EnvironmentVariable: "0"}, wantSession: false},
		{name: "invalid environment fails closed", info: releaseInfo(), env: map[string]string{EnvironmentVariable: "sometimes"}, wantSession: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			environment := test.env
			if environment == nil {
				environment = map[string]string{}
			}
			if test.preferences != "" {
				writeTestFile(t, settings.Path(home), test.preferences, 0o600)
			}
			session := Start(Config{
				Eligible:    true,
				HomeDir:     home,
				Endpoint:    "https://telemetry.example.test/v1/telemetry/batch",
				Info:        test.info,
				Environment: environment,
			})
			if (session != nil) != test.wantSession {
				t.Fatalf("session present = %t, want %t", session != nil, test.wantSession)
			}
			_, err := os.Stat(InstallationIDPath(home))
			if test.wantSession && err != nil {
				t.Fatalf("installation ID was not created: %v", err)
			}
			if !test.wantSession && !os.IsNotExist(err) {
				t.Fatalf("disabled telemetry created installation ID: %v", err)
			}
		})
	}
}

func TestStartShortCircuitsBeforeStateReads(t *testing.T) {
	for _, test := range []struct {
		name     string
		eligible bool
		endpoint string
		env      map[string]string
	}{
		{name: "ineligible", endpoint: "https://telemetry.example.test", env: map[string]string{EnvironmentVariable: "on"}},
		{name: "missing endpoint", eligible: true, env: map[string]string{EnvironmentVariable: "on"}},
		{name: "explicit disable", eligible: true, endpoint: "https://telemetry.example.test", env: map[string]string{EnvironmentVariable: "off"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			before := "not valid toml = ["
			writeTestFile(t, settings.Path(home), before, 0o600)
			writeTestFile(t, InstallationIDPath(home), "invalid-identity\n", 0o600)
			if session := Start(Config{Eligible: test.eligible, HomeDir: home, Endpoint: test.endpoint, Info: releaseInfo(), Environment: test.env}); session != nil {
				t.Fatal("expected telemetry to stay disabled")
			}
			assertFileContent(t, settings.Path(home), before)
			assertFileContent(t, InstallationIDPath(home), "invalid-identity\n")
		})
	}
}

func TestMalformedOrUnreadablePreferencesFailClosed(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		home := t.TempDir()
		before := "[telemetry]\nenabled = \"must-not-be-rewritten\"\n"
		writeTestFile(t, settings.Path(home), before, 0o600)
		if session := Start(Config{Eligible: true, HomeDir: home, Endpoint: "https://telemetry.example.test", Info: releaseInfo(), Environment: map[string]string{}}); session != nil {
			t.Fatal("malformed preferences should disable telemetry")
		}
		assertFileContent(t, settings.Path(home), before)
		if _, err := os.Stat(InstallationIDPath(home)); !os.IsNotExist(err) {
			t.Fatalf("malformed preferences created identity: %v", err)
		}
	})
	t.Run("unreadable state", func(t *testing.T) {
		home := t.TempDir()
		if err := os.MkdirAll(settings.Path(home), 0o700); err != nil {
			t.Fatal(err)
		}
		if session := Start(Config{Eligible: true, HomeDir: home, Endpoint: "https://telemetry.example.test", Info: releaseInfo(), Environment: map[string]string{}}); session != nil {
			t.Fatal("unreadable preferences should disable telemetry")
		}
		info, err := os.Stat(settings.Path(home))
		if err != nil || !info.IsDir() {
			t.Fatalf("preferences state was overwritten: info=%v err=%v", info, err)
		}
		if _, err := os.Stat(InstallationIDPath(home)); !os.IsNotExist(err) {
			t.Fatalf("unreadable preferences created identity: %v", err)
		}
	})
}

func TestReleaseDefaultDoesNotCreatePreferencesAndUserCanResetIdentity(t *testing.T) {
	home := t.TempDir()
	cfg := Config{Eligible: true, HomeDir: home, Endpoint: "https://telemetry.example.test", Info: releaseInfo(), Environment: map[string]string{}}
	first := Start(cfg)
	if first == nil {
		t.Fatal("release default should enable telemetry")
	}
	if _, err := os.Stat(settings.Path(home)); !os.IsNotExist(err) {
		t.Fatalf("default resolution created preferences: %v", err)
	}
	if err := os.Remove(InstallationIDPath(home)); err != nil {
		t.Fatal(err)
	}
	second := Start(cfg)
	if second == nil || second.installationID == first.installationID {
		t.Fatalf("identity reset did not create a new ID: first=%q second=%v", first.installationID, second)
	}
}

func TestPersistentDisableIsPreserved(t *testing.T) {
	home := t.TempDir()
	before := "# preserve user formatting\n[telemetry]\nenabled = false\n"
	writeTestFile(t, settings.Path(home), before, 0o644)
	if session := Start(Config{Eligible: true, HomeDir: home, Endpoint: "https://telemetry.example.test", Info: releaseInfo(), Environment: map[string]string{}}); session != nil {
		t.Fatal("persistent disable should disable telemetry")
	}
	assertFileContent(t, settings.Path(home), before)
	if _, err := os.Stat(InstallationIDPath(home)); !os.IsNotExist(err) {
		t.Fatalf("persistent disable created identity: %v", err)
	}
}

func TestInvalidEnvironmentOverrideFailsClosedWithRedactedDebug(t *testing.T) {
	home := t.TempDir()
	preferences := "[telemetry]\nenabled = true\n"
	identity := "invalid-identity\n"
	writeTestFile(t, settings.Path(home), preferences, 0o600)
	writeTestFile(t, InstallationIDPath(home), identity, 0o600)
	var diagnostic strings.Builder
	session := Start(Config{
		Eligible:    true,
		HomeDir:     home,
		Endpoint:    "https://telemetry.example.test",
		Info:        releaseInfo(),
		Environment: map[string]string{EnvironmentVariable: "secret-invalid-value"},
		Debug:       true,
		DebugWriter: &diagnostic,
	})
	if session != nil {
		t.Fatal("invalid override should disable telemetry")
	}
	if !strings.Contains(diagnostic.String(), "preference could not be resolved") || strings.Contains(diagnostic.String(), "secret-invalid-value") {
		t.Fatalf("unexpected debug diagnostic %q", diagnostic.String())
	}
	assertFileContent(t, settings.Path(home), preferences)
	assertFileContent(t, InstallationIDPath(home), identity)
}

func TestInstallationIDIsPrivateStableAndRaceSafe(t *testing.T) {
	home := t.TempDir()
	const workers = 32
	ids := make(chan string, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			session := Start(Config{
				Eligible:    true,
				HomeDir:     home,
				Endpoint:    "https://telemetry.example.test",
				Info:        releaseInfo(),
				Environment: map[string]string{EnvironmentVariable: "on"},
			})
			if session == nil {
				ids <- ""
				return
			}
			ids <- session.installationID
		}()
	}
	wait.Wait()
	close(ids)
	var expected string
	for id := range ids {
		if !installationIDPattern.MatchString(id) {
			t.Fatalf("invalid installation ID %q", id)
		}
		if expected == "" {
			expected = id
		} else if id != expected {
			t.Fatalf("concurrent initialization returned %q and %q", expected, id)
		}
	}
	data, err := os.ReadFile(InstallationIDPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != expected {
		t.Fatalf("persisted ID = %q, want %q", data, expected)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(InstallationIDPath(home))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("installation ID mode = %o, want 0600", info.Mode().Perm())
		}
	}
}

func TestMalformedOrUnreadableInstallationIDFailsClosed(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		home := t.TempDir()
		before := "ti_not-valid!\n"
		writeTestFile(t, InstallationIDPath(home), before, 0o600)
		if session := Start(enabledConfig(home)); session != nil {
			t.Fatal("malformed identity should disable telemetry")
		}
		assertFileContent(t, InstallationIDPath(home), before)
	})
	t.Run("unreadable state", func(t *testing.T) {
		home := t.TempDir()
		if err := os.MkdirAll(InstallationIDPath(home), 0o700); err != nil {
			t.Fatal(err)
		}
		if session := Start(enabledConfig(home)); session != nil {
			t.Fatal("unreadable identity state should disable telemetry")
		}
		info, err := os.Stat(InstallationIDPath(home))
		if err != nil || !info.IsDir() {
			t.Fatalf("identity state was overwritten: info=%v err=%v", info, err)
		}
	})
}

func TestFinishSendsOnlyAllowlistedEventFields(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/telemetry/batch" {
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := request.Header.Get("User-Agent"); got != "ti/0.2.0" {
			t.Errorf("User-Agent = %q", got)
		}
		body, _ := io.ReadAll(request.Body)
		requests <- body
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	home := t.TempDir()
	cfg := enabledConfig(home)
	cfg.Endpoint = server.URL + "/v1/telemetry/batch"
	cfg.Now = func() time.Time { return time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC) }
	session := Start(cfg)
	session.Finish(EventInput{
		CommandPath:   "ti db execute-sql-statement",
		FlagNames:     []string{"sql", "db-cluster-id", "tidb-cloud-private-key"},
		ExitCode:      2,
		ErrorCode:     "db.invalid_sql",
		Duration:      182 * time.Millisecond,
		CloudProvider: "aws",
		RegionCode:    "aws-us-east-1",
		ProfileSource: "explicit",
	})
	body := <-requests
	for _, prohibited := range []string{"SELECT secret_value", "private-key-value", "cluster-123", "profile-name", "raw_error", "file_path"} {
		if strings.Contains(string(body), prohibited) {
			t.Fatalf("payload contains prohibited value %q: %s", prohibited, body)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	events := decoded["events"].([]any)
	event := events[0].(map[string]any)
	if event["command_path"] != "ti db execute-sql-statement" || event["error_code"] != "db.invalid_sql" {
		t.Fatalf("unexpected event: %#v", event)
	}
	flags := event["flag_names"].([]any)
	if strings.Join([]string{flags[0].(string), flags[1].(string), flags[2].(string)}, ",") != "db-cluster-id,sql,tidb-cloud-private-key" {
		t.Fatalf("flags were not canonical names only: %#v", flags)
	}
}

func TestTelemetryEnvironmentMetadataIsBoundedAndNotPersisted(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		requests <- body
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	home := t.TempDir()
	cfg := enabledConfig(home)
	cfg.Endpoint = server.URL
	cfg.Environment[TagEnvironmentVariable] = strings.Repeat("a", 127) + "界"
	cfg.Environment[ExtraEnvironmentVariable] = ` { "campaign" : "launch" , "runtime" : "e2b" } `
	session := Start(cfg)
	if session == nil {
		t.Fatal("telemetry session was not created")
	}
	session.Finish(EventInput{CommandPath: "ti db list-db-clusters"})
	body := <-requests
	var payload struct {
		SchemaVersion int `json:"schema_version"`
		Events        []struct {
			Tag   string          `json:"tag"`
			Extra json.RawMessage `json:"extra"`
		} `json:"events"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SchemaVersion != 2 || len(payload.Events) != 1 {
		t.Fatalf("payload = %s", body)
	}
	if got := payload.Events[0].Tag; len(got) > maxTagBytes || !utf8.ValidString(got) {
		t.Fatalf("tag = %q (%d bytes), want valid bounded value", got, len(got))
	}
	if got := string(payload.Events[0].Extra); got != `{"campaign":"launch","runtime":"e2b"}` {
		t.Fatalf("extra = %s", got)
	}
	for _, path := range []string{settings.Path(home), store.ConfigPath(home), filepath.Join(home, ".ti", "credentials")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("metadata unexpectedly persisted to %s: %v", path, err)
		}
	}
}

func TestInvalidTelemetryEnvironmentMetadataIsOmittedWithoutLeaking(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		requests <- body
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	home := t.TempDir()
	var diagnostic strings.Builder
	cfg := enabledConfig(home)
	cfg.Endpoint = server.URL
	cfg.Debug = true
	cfg.DebugWriter = &diagnostic
	cfg.Environment[TagEnvironmentVariable] = string([]byte{0xff})
	cfg.Environment[ExtraEnvironmentVariable] = `{"password":"must-not-leak"}`
	Start(cfg).Finish(EventInput{CommandPath: "ti db list-db-clusters"})
	body := <-requests
	if strings.Contains(string(body), "must-not-leak") || strings.Contains(diagnostic.String(), "must-not-leak") {
		t.Fatalf("metadata leaked: payload=%s debug=%s", body, diagnostic.String())
	}
	var payload struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if _, exists := payload.Events[0]["tag"]; exists {
		t.Fatalf("invalid tag was sent: %s", body)
	}
	if _, exists := payload.Events[0]["extra"]; exists {
		t.Fatalf("invalid extra was sent: %s", body)
	}
}

func TestTelemetryEnvironmentMetadataIsNotReadWhenDisabled(t *testing.T) {
	home := t.TempDir()
	cfg := enabledConfig(home)
	cfg.Environment[EnvironmentVariable] = "off"
	cfg.Environment[ExtraEnvironmentVariable] = `{"password":"must-not-parse"}`
	if session := Start(cfg); session != nil {
		t.Fatal("disabled telemetry created a session")
	}
	if _, err := os.Stat(InstallationIDPath(home)); !os.IsNotExist(err) {
		t.Fatalf("disabled telemetry wrote state: %v", err)
	}
}

func TestNormalizeTelemetryMetadata(t *testing.T) {
	t.Run("tag", func(t *testing.T) {
		for _, test := range []struct {
			name  string
			value string
			ok    bool
		}{
			{name: "absent", ok: true},
			{name: "exact", value: "agent-demo", ok: true},
			{name: "multibyte truncation", value: strings.Repeat("界", 50), ok: true},
			{name: "invalid utf8", value: string([]byte{0xff}), ok: false},
		} {
			t.Run(test.name, func(t *testing.T) {
				got, ok := normalizeTag(test.value, test.name != "absent")
				if ok != test.ok || (ok && (len(got) > maxTagBytes || !utf8.ValidString(got))) {
					t.Fatalf("normalizeTag(%q) = %q, %t", test.value, got, ok)
				}
			})
		}
	})

	for _, test := range []struct {
		name  string
		value string
		ok    bool
		want  string
	}{
		{name: "object", value: ` { "campaign" : "launch" } `, ok: true, want: `{"campaign":"launch"}`},
		{name: "array", value: `[1, "two"]`, ok: true, want: `[1,"two"]`},
		{name: "number", value: `1.25`, ok: true, want: `1.25`},
		{name: "boolean", value: `true`, ok: true, want: `true`},
		{name: "null", value: `null`, ok: true, want: `null`},
		{name: "malformed", value: `{`, ok: false},
		{name: "multiple values", value: `{} {}`, ok: false},
		{name: "prohibited key", value: `{"token":"secret"}`, ok: false},
		{name: "oversized", value: `"` + strings.Repeat("a", maxExtraBytes) + `"`, ok: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := normalizeExtra(test.value, true)
			if ok != test.ok || (ok && string(got) != test.want) {
				t.Fatalf("normalizeExtra(%q) = %s, %t", test.value, got, ok)
			}
		})
	}
	tooDeep := `"leaf"`
	for range maxExtraDepth + 1 {
		tooDeep = `{"level":` + tooDeep + `}`
	}
	if got, ok := normalizeExtra(tooDeep, true); ok || got != nil {
		t.Fatalf("too-deep extra was accepted: %s", got)
	}
}

func TestDeliveryFailuresAreSilentByDefault(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusInternalServerError} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte("sensitive backend response"))
		}))
		home := t.TempDir()
		cfg := enabledConfig(home)
		cfg.Endpoint = server.URL
		var debug strings.Builder
		cfg.DebugWriter = &debug
		Start(cfg).Finish(EventInput{CommandPath: "ti organization list-projects"})
		server.Close()
		if debug.Len() != 0 {
			t.Fatalf("status %d produced normal output: %q", status, debug.String())
		}
	}
}

func TestDeliveryDoesNotFollowRedirects(t *testing.T) {
	redirected := false
	sink := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		redirected = true
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer sink.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", sink.URL)
		writer.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	cfg := enabledConfig(t.TempDir())
	cfg.Endpoint = server.URL
	Start(cfg).Finish(EventInput{CommandPath: "ti organization list-projects"})
	if redirected {
		t.Fatal("telemetry followed a redirect away from its configured endpoint")
	}
}

func enabledConfig(home string) Config {
	return Config{
		Eligible:    true,
		HomeDir:     home,
		Endpoint:    "https://telemetry.example.test/v1/telemetry/batch",
		Info:        releaseInfo(),
		Environment: map[string]string{EnvironmentVariable: "on"},
	}
}

func releaseInfo() version.Info {
	return version.Info{Version: "0.2.0", OS: "linux", Arch: "amd64", InstallSource: "archive"}
}

func writeTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s changed: got %q, want %q", path, data, want)
	}
}
