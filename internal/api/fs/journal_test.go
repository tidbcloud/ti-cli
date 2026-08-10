package fs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJournalClientMethods(t *testing.T) {
	var sawAppendKey string
	var sawSearchMeta []string
	var sawSearchInclude string
	var sawSearchSince string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer fs-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/journals":
			var req JournalCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode create request: %v", err)
			}
			if req.JournalID != "jrn_client" || req.Kind != "agent" || len(req.Labels) != 1 || req.Labels[0].Key != "env" {
				t.Fatalf("unexpected create request: %#v", req)
			}
			_ = json.NewEncoder(w).Encode(Journal{JournalID: "jrn_client", Kind: "agent"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/journals/jrn_client/entries":
			sawAppendKey = r.Header.Get("Idempotency-Key")
			var req []JournalEntryInput
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode append request: %v", err)
			}
			if len(req) != 1 || req[0].Type != "tool.call.completed" {
				t.Fatalf("unexpected append request: %#v", req)
			}
			_ = json.NewEncoder(w).Encode(JournalAppendResponse{JournalID: "jrn_client", AppendID: sawAppendKey, FirstSeq: 1, LastSeq: 1, Count: 1, HeadHash: "sha256:abc", Idempotent: true})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/journals/jrn_client/entries":
			if r.URL.Query().Get("after_seq") != "1" || r.URL.Query().Get("limit") != "10" {
				t.Fatalf("unexpected read query: %q", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = w.Write([]byte(`{"journal_id":"jrn_client","seq":2,"entry_id":"jre_2","type":"tool.call.completed"}` + "\n"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/journal-entries":
			sawSearchMeta = append([]string(nil), r.URL.Query()["meta"]...)
			sawSearchInclude = r.URL.Query().Get("include")
			sawSearchSince = r.URL.Query().Get("since")
			if r.URL.Query().Get("actor") != "agent:ti" || r.URL.Query().Get("type") != "tool.call.completed" {
				t.Fatalf("unexpected search query: %q", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = w.Write([]byte(`{"journal_id":"jrn_client","seq":2,"type":"tool.call.completed","cursor":"cur"}` + "\n"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/journals/jrn_client/verify":
			_ = json.NewEncoder(w).Encode(JournalVerifyResult{OK: true, JournalID: "jrn_client", Entries: 1, HeadHash: "sha256:abc", HashChainOK: true})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	client := testBearerClient(t, server.URL)
	ctx := context.Background()
	created, err := client.CreateJournal(ctx, JournalCreateRequest{JournalID: "jrn_client", Kind: "agent", Labels: []JournalLabel{{Key: "env", Value: "prod"}}})
	if err != nil || created.JournalID != "jrn_client" {
		t.Fatalf("CreateJournal = %#v, %v", created, err)
	}
	appended, err := client.AppendJournalEntries(ctx, "jrn_client", "app_client", []JournalEntryInput{{Type: "tool.call.completed"}})
	if err != nil || appended.AppendID != "app_client" {
		t.Fatalf("AppendJournalEntries = %#v, %v", appended, err)
	}
	if sawAppendKey != "app_client" {
		t.Fatalf("Idempotency-Key = %q", sawAppendKey)
	}
	entries, err := client.ReadJournalEntries(ctx, "jrn_client", 1, 10)
	if err != nil || len(entries) != 1 || entries[0].Seq != 2 {
		t.Fatalf("ReadJournalEntries = %#v, %v", entries, err)
	}
	matches, err := client.SearchJournal(ctx, JournalSearchRequest{
		Type:      "tool.call.completed",
		ActorType: "agent",
		ActorID:   "ti",
		Labels:    []JournalLabel{{Key: "env", Value: "prod"}, {Key: "env", Value: "us-east"}},
		SinceRaw:  "1h",
		Entries:   true,
	})
	if err != nil || len(matches) != 1 || matches[0].Cursor != "cur" {
		t.Fatalf("SearchJournal = %#v, %v", matches, err)
	}
	if len(sawSearchMeta) != 2 || sawSearchMeta[0] != "env=prod" || sawSearchMeta[1] != "env=us-east" {
		t.Fatalf("search meta query = %#v", sawSearchMeta)
	}
	if sawSearchInclude != "entry" || sawSearchSince != "1h" {
		t.Fatalf("include=%q since=%q", sawSearchInclude, sawSearchSince)
	}
	verified, err := client.VerifyJournal(ctx, "jrn_client")
	if err != nil || !verified.OK {
		t.Fatalf("VerifyJournal = %#v, %v", verified, err)
	}
}
