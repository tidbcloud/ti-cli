package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestTelemetryDeliveryToTiDB(t *testing.T) {
	if os.Getenv("TDC_TELEMETRY_E2E") != "1" {
		t.Skip("run make telemetry-e2e")
	}
	baseDSN := strings.TrimSpace(os.Getenv("TDC_TEST_TELEMETRY_TIDB_DSN"))
	if baseDSN == "" {
		t.Fatal("TDC_TEST_TELEMETRY_TIDB_DSN is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	databaseDSN, databaseName := createTelemetryE2EDatabase(t, ctx, baseDSN)
	testDB, err := sql.Open("mysql", databaseDSN)
	if err != nil {
		t.Fatal("open telemetry e2e database")
	}
	defer testDB.Close()

	migrator := telemetryMigratorBinary(t)
	runTelemetryMigrator(t, ctx, migrator, databaseDSN, "--to-version", "1")
	if _, err := testDB.ExecContext(ctx, `INSERT INTO telemetry_events (
event_id, occurred_at, anonymous_installation_id, event_name, command_path,
flag_names_json, exit_code, duration_ms, cli_version, os, arch, schema_version
) VALUES (?, CURRENT_TIMESTAMP(6), ?, ?, ?, JSON_ARRAY(), 0, 0, ?, ?, ?, 1)`,
		"legacy-event", "tdc_legacy_installation", "tdc.command.finished", "tdc legacy", "0.0.0", "linux", "amd64"); err != nil {
		t.Fatalf("insert legacy telemetry event: %v", err)
	}
	runTelemetryMigrator(t, ctx, migrator, databaseDSN)
	assertLegacyEventSurvivesMigration(t, ctx, testDB)

	postHogRequests := make(chan struct{}, 1)
	postHog := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/batch/" {
			http.NotFound(writer, request)
			return
		}
		select {
		case postHogRequests <- struct{}{}:
		default:
		}
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(postHog.Close)

	endpoint := startTelemetryBackend(t, ctx, databaseDSN, postHog.URL)
	tag := "telemetry-e2e-" + databaseName
	runID := "run-" + tag
	result := runTDCWithInput(t, tdcBinary(t), "", append(tdcConfigEnv(),
		"HOME="+t.TempDir(),
		"TDC_ALLOW_TEST_ENDPOINTS=1",
		"TDC_TEST_TELEMETRY_ENDPOINT="+endpoint+"/v1/telemetry/batch",
		"TDC_TELEMETRY=on",
		"TDC_TELEMETRY_TAG="+tag,
		`TDC_TELEMETRY_EXTRA={"telemetry_e2e_run":"`+runID+`"}`,
	), createClusterDryRunArgs()...)
	result.wantExitCode(0)

	assertTelemetryEventStored(t, ctx, testDB, tag, runID)
	select {
	case <-postHogRequests:
	case <-time.After(10 * time.Second):
		t.Fatal("local PostHog receiver did not receive the telemetry batch")
	}
}

func createTelemetryE2EDatabase(t *testing.T, ctx context.Context, baseDSN string) (string, string) {
	t.Helper()
	config, err := mysql.ParseDSN(baseDSN)
	if err != nil {
		t.Fatal("parse TDC_TEST_TELEMETRY_TIDB_DSN")
	}
	adminConfig := *config
	adminConfig.DBName = ""
	adminDB, err := sql.Open("mysql", adminConfig.FormatDSN())
	if err != nil {
		t.Fatal("open TiDB server connection")
	}
	t.Cleanup(func() { _ = adminDB.Close() })
	if err := adminDB.PingContext(ctx); err != nil {
		t.Fatalf("connect to TiDB server: %v", err)
	}
	name := "tdc_telemetry_e2e_" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405000000000"), "-", "")
	if _, err := adminDB.ExecContext(ctx, "CREATE DATABASE `"+name+"`"); err != nil {
		t.Fatalf("create isolated telemetry e2e database: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := adminDB.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS `"+name+"`"); err != nil {
			t.Errorf("drop isolated telemetry e2e database: %v", err)
		}
	})
	testConfig := *config
	testConfig.DBName = name
	return testConfig.FormatDSN(), name
}

func telemetryMigratorBinary(t *testing.T) string {
	t.Helper()
	path := strings.TrimSpace(os.Getenv("TDC_TELEMETRY_MIGRATOR_E2E_BIN"))
	if path == "" {
		t.Fatal("TDC_TELEMETRY_MIGRATOR_E2E_BIN is required; run make telemetry-e2e")
	}
	return path
}

func runTelemetryMigrator(t *testing.T, ctx context.Context, binary, dsn string, args ...string) {
	t.Helper()
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = append(os.Environ(), "TIDB_DSN="+dsn)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run telemetry migrator: %v\n%s", err, output)
	}
}

func assertLegacyEventSurvivesMigration(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var (
		commandPath string
		tag         string
		extra       any
	)
	if err := db.QueryRowContext(ctx, "SELECT command_path, tag, extra_json FROM telemetry_events WHERE event_id = 'legacy-event'").Scan(&commandPath, &tag, &extra); err != nil {
		t.Fatalf("read legacy event after migration: %v", err)
	}
	if commandPath != "tdc legacy" || tag != "" || extra != nil {
		t.Fatalf("legacy event changed during migration: command=%q tag=%q extra=%#v", commandPath, tag, extra)
	}
}

func startTelemetryBackend(t *testing.T, ctx context.Context, dsn, postHogURL string) string {
	t.Helper()
	binary := strings.TrimSpace(os.Getenv("TDC_TELEMETRY_BACKEND_E2E_BIN"))
	if binary == "" {
		t.Fatal("TDC_TELEMETRY_BACKEND_E2E_BIN is required; run make telemetry-e2e")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve telemetry backend port: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release telemetry backend port: %v", err)
	}

	command := exec.Command(binary)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	command.Env = append(os.Environ(),
		"TIDB_DSN="+dsn,
		"POSTHOG_API_HOST="+postHogURL,
		"POSTHOG_PROJECT_TOKEN=telemetry-e2e",
		"TELEMETRY_BIND_ADDR="+address,
		"TELEMETRY_PUBLIC_HOST=telemetry-e2e.local",
		"TELEMETRY_ENVIRONMENT=telemetry-e2e",
		"TELEMETRY_FLUSH_MAX_EVENTS=1",
		"TELEMETRY_FLUSH_INTERVAL=1h",
	)
	if err := command.Start(); err != nil {
		t.Fatalf("start telemetry backend: %v", err)
	}
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Signal(syscall.SIGTERM)
		}
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Errorf("stop telemetry backend: %v\n%s", err, stderr.String())
			}
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			if err := <-done; err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Errorf("force-stop telemetry backend: %v\n%s", err, stderr.String())
			}
		}
	})

	readyURL := "http://" + address + "/readyz"
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, readyURL, nil)
		if err == nil {
			response, err := http.DefaultClient.Do(request)
			if err == nil {
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return "http://" + address
				}
			}
		}
		if ctx.Err() != nil {
			t.Fatalf("telemetry backend did not become ready: %v\n%s", ctx.Err(), stderr.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func assertTelemetryEventStored(t *testing.T, ctx context.Context, db *sql.DB, tag, runID string) {
	t.Helper()
	var (
		commandPath string
		exitCode    int
		schema      int
		extra       []byte
	)
	for {
		err := db.QueryRowContext(ctx, `SELECT command_path, exit_code, schema_version, extra_json
FROM telemetry_events WHERE tag = ? ORDER BY received_at DESC LIMIT 1`, tag).Scan(&commandPath, &exitCode, &schema, &extra)
		if err == nil {
			break
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("query telemetry e2e record: %v", err)
		}
		if ctx.Err() != nil {
			t.Fatalf("timed out waiting for telemetry event with tag %q", tag)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var metadata map[string]string
	if err := json.Unmarshal(extra, &metadata); err != nil {
		t.Fatalf("decode stored telemetry extra metadata: %v", err)
	}
	if commandPath != "tdc db create-db-cluster" || exitCode != 0 || schema != 2 || metadata["telemetry_e2e_run"] != runID {
		t.Fatalf("unexpected stored telemetry event: command=%q exit_code=%d schema=%d extra=%s", commandPath, exitCode, schema, extra)
	}
}
