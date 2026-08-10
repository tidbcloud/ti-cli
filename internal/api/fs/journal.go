package fs

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

const (
	DefaultJournalKind  = "agent"
	DefaultJournalLimit = 100
	MaxJournalLimit     = 1000
)

type JournalActor struct {
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"`
}

type JournalLabel struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type JournalCreateRequest struct {
	JournalID string            `json:"journal_id,omitempty"`
	Kind      string            `json:"kind,omitempty"`
	Title     string            `json:"title,omitempty"`
	Actor     JournalActor      `json:"actor,omitempty"`
	Source    string            `json:"source,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
	Labels    []JournalLabel    `json:"labels,omitempty"`
	Retention json.RawMessage   `json:"retention,omitempty"`
}

type Journal struct {
	TenantID    string            `json:"tenant_id,omitempty"`
	JournalID   string            `json:"journal_id"`
	Kind        string            `json:"kind"`
	Title       string            `json:"title,omitempty"`
	Actor       JournalActor      `json:"actor,omitempty"`
	Source      string            `json:"source,omitempty"`
	Meta        map[string]string `json:"meta,omitempty"`
	Labels      []JournalLabel    `json:"labels,omitempty"`
	Retention   json.RawMessage   `json:"retention,omitempty"`
	NextSeq     int64             `json:"next_seq,omitempty"`
	GenesisHash string            `json:"genesis_hash,omitempty"`
	HeadHash    string            `json:"head_hash,omitempty"`
	CreatedAt   time.Time         `json:"created_at,omitempty"`
	UpdatedAt   time.Time         `json:"updated_at,omitempty"`
	ClosedAt    *time.Time        `json:"closed_at,omitempty"`
}

type JournalArtifactRef struct {
	Name        string `json:"name"`
	Hash        string `json:"hash"`
	ContentType string `json:"content_type,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`
}

type JournalEntryInput struct {
	Type          string               `json:"type,omitempty"`
	SchemaVersion int                  `json:"schema_version,omitempty"`
	Status        string               `json:"status,omitempty"`
	OccurredAt    *time.Time           `json:"occurred_at,omitempty"`
	Actor         JournalActor         `json:"actor,omitempty"`
	Source        string               `json:"source,omitempty"`
	ParentEntryID string               `json:"parent_entry_id,omitempty"`
	CorrelationID string               `json:"correlation_id,omitempty"`
	Subjects      []string             `json:"subjects,omitempty"`
	Summary       json.RawMessage      `json:"summary,omitempty"`
	Artifacts     []JournalArtifactRef `json:"artifacts,omitempty"`
	ArtifactRefs  []JournalArtifactRef `json:"artifact_refs,omitempty"`
}

type JournalEntry struct {
	TenantID      string               `json:"tenant_id,omitempty"`
	JournalID     string               `json:"journal_id"`
	Seq           int64                `json:"seq"`
	EntryID       string               `json:"entry_id"`
	Type          string               `json:"type"`
	SchemaVersion int                  `json:"schema_version"`
	Status        string               `json:"status,omitempty"`
	OccurredAt    time.Time            `json:"occurred_at,omitempty"`
	ObservedAt    time.Time            `json:"observed_at,omitempty"`
	Actor         JournalActor         `json:"actor,omitempty"`
	Source        string               `json:"source,omitempty"`
	ParentEntryID string               `json:"parent_entry_id,omitempty"`
	CorrelationID string               `json:"correlation_id,omitempty"`
	Subjects      []string             `json:"subjects,omitempty"`
	Summary       json.RawMessage      `json:"summary,omitempty"`
	ArtifactRefs  []JournalArtifactRef `json:"artifact_refs,omitempty"`
	PrevHash      string               `json:"prev_hash,omitempty"`
	EntryHash     string               `json:"entry_hash,omitempty"`
}

type JournalAppendResponse struct {
	JournalID  string `json:"journal_id"`
	AppendID   string `json:"append_id"`
	FirstSeq   int64  `json:"first_seq"`
	LastSeq    int64  `json:"last_seq"`
	Count      int    `json:"count"`
	HeadHash   string `json:"head_hash"`
	Idempotent bool   `json:"idempotent"`
}

type JournalSearchRequest struct {
	Type      string
	Status    string
	Kind      string
	ActorType string
	ActorID   string
	Subjects  []string
	Labels    []JournalLabel
	SinceRaw  string
	UntilRaw  string
	Limit     int
	Entries   bool
	Cursor    string
}

type JournalSearchMatch struct {
	JournalID       string         `json:"journal_id"`
	Seq             int64          `json:"seq,omitempty"`
	Type            string         `json:"type,omitempty"`
	Status          string         `json:"status,omitempty"`
	Kind            string         `json:"kind,omitempty"`
	Title           string         `json:"title,omitempty"`
	ObservedAt      time.Time      `json:"observed_at,omitempty"`
	CreatedAt       time.Time      `json:"created_at,omitempty"`
	MatchedSubjects []string       `json:"matched_subjects,omitempty"`
	MatchedLabels   []JournalLabel `json:"matched_labels,omitempty"`
	Cursor          string         `json:"cursor,omitempty"`
	Entry           *JournalEntry  `json:"entry,omitempty"`
}

type JournalVerifyResult struct {
	OK                     bool   `json:"ok"`
	JournalID              string `json:"journal_id"`
	Entries                int64  `json:"entries"`
	HeadHash               string `json:"head_hash"`
	HashChainOK            bool   `json:"hash_chain_ok"`
	SealOK                 *bool  `json:"seal_ok,omitempty"`
	ProjectionOK           *bool  `json:"projection_ok,omitempty"`
	ArtifactBytesAvailable *bool  `json:"artifact_bytes_available,omitempty"`
	HeadSealed             *bool  `json:"head_sealed,omitempty"`
	LatestSealSeq          *int64 `json:"latest_seal_seq,omitempty"`
}

func (c *Client) CreateJournal(ctx context.Context, request JournalCreateRequest) (Journal, error) {
	var response Journal
	if err := c.doVaultJSON(ctx, http.MethodPost, "/v1/journals", request, &response); err != nil {
		return Journal{}, err
	}
	return response, nil
}

func (c *Client) AppendJournalEntries(ctx context.Context, journalID, appendID string, entries []JournalEntryInput) (JournalAppendResponse, error) {
	req, err := c.api.NewRequest(ctx, http.MethodPost, "/v1/journals/"+url.PathEscape(journalID)+"/entries", entries)
	if err != nil {
		return JournalAppendResponse{}, err
	}
	req.Header.Set("Idempotency-Key", appendID)
	var response JournalAppendResponse
	if err := c.api.DoJSON(req, &response); err != nil {
		return JournalAppendResponse{}, err
	}
	return response, nil
}

func (c *Client) ReadJournalEntries(ctx context.Context, journalID string, afterSeq int64, limit int) ([]JournalEntry, error) {
	values := url.Values{}
	if afterSeq > 0 {
		values.Set("after_seq", strconv.FormatInt(afterSeq, 10))
	}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	requestPath := "/v1/journals/" + url.PathEscape(journalID) + "/entries"
	if encoded := values.Encode(); encoded != "" {
		requestPath += "?" + encoded
	}
	req, err := c.api.NewRequest(ctx, http.MethodGet, requestPath, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var entries []JournalEntry
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry JournalEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, apperr.Wrap("journal.decode_entry", "runtime", 1, "decode journal entry", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, apperr.Wrap("journal.read_entries", "runtime", 1, "read journal entries", err)
	}
	if entries == nil {
		entries = []JournalEntry{}
	}
	return entries, nil
}

func (c *Client) SearchJournal(ctx context.Context, request JournalSearchRequest) ([]JournalSearchMatch, error) {
	values := url.Values{}
	if request.Type != "" {
		values.Set("type", request.Type)
	}
	if request.Status != "" {
		values.Set("status", request.Status)
	}
	if request.Kind != "" {
		values.Set("kind", request.Kind)
	}
	if request.ActorType != "" {
		values.Set("actor", request.ActorType+":"+request.ActorID)
	}
	for _, subject := range request.Subjects {
		values.Add("subject", subject)
	}
	for _, label := range NormalizeJournalLabels(request.Labels) {
		values.Add("meta", label.Key+"="+label.Value)
	}
	if request.SinceRaw != "" {
		values.Set("since", request.SinceRaw)
	}
	if request.UntilRaw != "" {
		values.Set("until", request.UntilRaw)
	}
	if request.Limit > 0 {
		values.Set("limit", strconv.Itoa(request.Limit))
	}
	if request.Cursor != "" {
		values.Set("cursor", request.Cursor)
	}
	if request.Entries {
		values.Set("include", "entry")
	}
	requestPath := "/v1/journal-entries"
	if encoded := values.Encode(); encoded != "" {
		requestPath += "?" + encoded
	}
	req, err := c.api.NewRequest(ctx, http.MethodGet, requestPath, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var matches []JournalSearchMatch
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var match JournalSearchMatch
		if err := json.Unmarshal([]byte(line), &match); err != nil {
			return nil, apperr.Wrap("journal.decode_search_match", "runtime", 1, "decode journal search match", err)
		}
		matches = append(matches, match)
	}
	if err := scanner.Err(); err != nil {
		return nil, apperr.Wrap("journal.read_search", "runtime", 1, "read journal search matches", err)
	}
	if matches == nil {
		matches = []JournalSearchMatch{}
	}
	return matches, nil
}

func (c *Client) VerifyJournal(ctx context.Context, journalID string) (JournalVerifyResult, error) {
	var response JournalVerifyResult
	if err := c.doVaultJSON(ctx, http.MethodGet, "/v1/journals/"+url.PathEscape(journalID)+"/verify", nil, &response); err != nil {
		return JournalVerifyResult{}, err
	}
	return response, nil
}

func NormalizeJournalLabels(labels []JournalLabel) []JournalLabel {
	out := make([]JournalLabel, 0, len(labels))
	for _, label := range labels {
		key := strings.ToLower(strings.TrimSpace(label.Key))
		value := strings.TrimSpace(label.Value)
		if key == "" && value == "" {
			continue
		}
		out = append(out, JournalLabel{Key: key, Value: value})
	}
	return out
}

func SplitJournalActor(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", nil
	}
	actorType, actorID, ok := strings.Cut(raw, ":")
	if !ok || strings.TrimSpace(actorType) == "" || strings.TrimSpace(actorID) == "" {
		return "", "", apperr.New("journal.invalid_actor", "usage", 2, fmt.Sprintf("actor %q must be in the form type:id", raw))
	}
	return strings.TrimSpace(actorType), strings.TrimSpace(actorID), nil
}
