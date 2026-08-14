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

func TestRenderTextRejectsMissingFormatter(t *testing.T) {
	var out bytes.Buffer
	err := Render(&out, map[string]any{"id": "cluster-1"}, Options{Format: FormatText})
	if apperr.CodeFor(err) != "output.text_formatter_missing" {
		t.Fatalf("error = %v", err)
	}
}

func TestRenderQueryText(t *testing.T) {
	tests := []struct {
		name  string
		query string
		value any
		want  string
	}{
		{name: "scalar", query: "id", value: map[string]any{"id": "cluster-1"}, want: "cluster-1\n"},
		{name: "scalar list", query: "ids", value: map[string]any{"ids": []string{"one", "two"}}, want: "one\ntwo\n"},
		{name: "object", query: "item", value: map[string]any{"item": map[string]any{"state": "ACTIVE", "id": "one"}}, want: "id     one\nstate  ACTIVE\n"},
		{name: "object list", query: "items", value: map[string]any{"items": []map[string]any{{"id": "one", "state": "ACTIVE"}, {"id": "two", "state": "PAUSED"}}}, want: "id   state\none  ACTIVE\ntwo  PAUSED\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := Render(&out, tt.value, Options{Format: FormatText, Query: tt.query}); err != nil {
				t.Fatal(err)
			}
			if got := out.String(); got != tt.want {
				t.Fatalf("output = %q, want %q", got, tt.want)
			}
		})
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
