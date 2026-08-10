package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

type testHuman struct {
	ID string `json:"id"`
}

func (h testHuman) Human() string {
	return "ID: " + h.ID
}

func TestRenderJSONByDefault(t *testing.T) {
	var out bytes.Buffer
	err := Render(&out, map[string]any{"id": "cluster-1"}, Options{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := out.String(); !strings.Contains(got, `"id": "cluster-1"`) {
		t.Fatalf("unexpected output:\n%s", got)
	}
}

func TestRenderText(t *testing.T) {
	var out bytes.Buffer
	err := Render(&out, testHuman{ID: "cluster-1"}, Options{Format: FormatText})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := out.String(); got != "ID: cluster-1\n" {
		t.Fatalf("unexpected output %q", got)
	}
}

func TestRenderAppliesQueryBeforeRendering(t *testing.T) {
	var out bytes.Buffer
	value := map[string]any{
		"clusters": []map[string]any{
			{"id": "cluster-1"},
		},
	}
	err := Render(&out, value, Options{Query: "clusters[0].id"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := out.String(); got != "\"cluster-1\"\n" {
		t.Fatalf("unexpected output %q", got)
	}
}

func TestRenderInvalidQuery(t *testing.T) {
	var out bytes.Buffer
	err := Render(&out, map[string]any{"clusters": []any{}}, Options{Query: "clusters["})
	if err == nil {
		t.Fatal("expected invalid query to fail")
	}
	if got := apperr.ExitCodeFor(err); got != 2 {
		t.Fatalf("expected exit code 2, got %d", got)
	}
	if got := apperr.MessageFor(err); !strings.Contains(got, "invalid --query expression") {
		t.Fatalf("unexpected message %q", got)
	}
}

func TestRenderRejectsQueryForRawOutput(t *testing.T) {
	var out bytes.Buffer
	err := Render(&out, Raw{Bytes: []byte("file bytes")}, Options{Query: "id"})
	if err == nil {
		t.Fatal("expected raw query to fail")
	}
	if got := apperr.MessageFor(err); !strings.Contains(got, "requires structured output") {
		t.Fatalf("unexpected message %q", got)
	}
}
