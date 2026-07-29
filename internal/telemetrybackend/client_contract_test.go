package telemetrybackend

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	clienttelemetry "github.com/tidbcloud/tdc/internal/telemetry"
	"github.com/tidbcloud/tdc/internal/version"
)

func TestCLIEventMatchesBackendContract(t *testing.T) {
	validation := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err == nil {
			_, err = decodeAndValidateBatch(body, 20, time.Now().UTC())
		}
		validation <- err
		if err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	session := clienttelemetry.Start(clienttelemetry.Config{
		Eligible:    true,
		HomeDir:     t.TempDir(),
		Endpoint:    server.URL,
		Info:        version.Info{Version: "0.2.0", OS: "linux", Arch: "amd64", InstallSource: "archive"},
		Environment: map[string]string{clienttelemetry.EnvironmentVariable: "on"},
	})
	if session == nil {
		t.Fatal("telemetry session was not created")
	}
	session.Finish(clienttelemetry.EventInput{
		CommandPath:   "tdc fs create-file-system",
		FlagNames:     []string{"file-system-name", "wait"},
		ExitCode:      0,
		Duration:      150 * time.Millisecond,
		CloudProvider: "aws",
		RegionCode:    "aws-us-east-1",
		ProfileSource: "default",
	})
	if err := <-validation; err != nil {
		t.Fatalf("CLI payload did not satisfy backend schema: %v", err)
	}
}
