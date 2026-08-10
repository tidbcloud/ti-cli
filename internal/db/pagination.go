package db

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	apistarter "github.com/tidbcloud/ti-cli/internal/api/starter"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
)

const (
	defaultResultPageSize   int32 = 10
	maximumResultPageSize   int32 = 1000
	upstreamClusterPageSize int32 = 100
	maximumPageTokenLength        = 16 * 1024
	pageTokenSchemaVersion        = 1
)

type listCursor struct {
	Version           int         `json:"v"`
	ProfileName       string      `json:"profile"`
	ClusterType       ClusterType `json:"cluster_type"`
	RegionCode        string      `json:"region_code"`
	Filter            string      `json:"filter,omitempty"`
	OrderBy           string      `json:"order_by,omitempty"`
	UpstreamPageSize  int32       `json:"upstream_page_size"`
	UpstreamPageToken string      `json:"upstream_page_token,omitempty"`
	MatchedOffset     int         `json:"matched_offset,omitempty"`
	PageFingerprint   string      `json:"page_fingerprint,omitempty"`
}

func resultPageSize(value int32) (int, error) {
	if value == 0 {
		return int(defaultResultPageSize), nil
	}
	if value < 0 || value > maximumResultPageSize {
		return 0, apperr.New("db.invalid_page_size", "usage", 2, fmt.Sprintf("--page-size must be between 1 and %d", maximumResultPageSize))
	}
	return int(value), nil
}

func newListCursor(opts ListClustersOptions, clusterType ClusterType) listCursor {
	return listCursor{
		Version:          pageTokenSchemaVersion,
		ProfileName:      normalizedProfileName(opts.Profile),
		ClusterType:      clusterType,
		RegionCode:       effectiveRegionCode(opts),
		Filter:           opts.Filter,
		OrderBy:          opts.OrderBy,
		UpstreamPageSize: upstreamClusterPageSize,
	}
}

func decodeListCursor(raw string, expected listCursor) (listCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return expected, nil
	}
	if len(raw) > maximumPageTokenLength {
		return listCursor{}, invalidPageToken("page token is too large")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(data) > maximumPageTokenLength {
		return listCursor{}, invalidPageToken("page token is not valid Base64URL")
	}
	var cursor listCursor
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return listCursor{}, invalidPageToken("page token payload is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return listCursor{}, invalidPageToken("page token payload contains trailing data")
	}
	if cursor.Version != pageTokenSchemaVersion || cursor.UpstreamPageSize != upstreamClusterPageSize || cursor.MatchedOffset < 0 {
		return listCursor{}, invalidPageToken("page token version or position is invalid")
	}
	if cursor.ProfileName != expected.ProfileName || cursor.ClusterType != expected.ClusterType || cursor.RegionCode != expected.RegionCode || cursor.Filter != expected.Filter || cursor.OrderBy != expected.OrderBy {
		return listCursor{}, apperr.New("db.page_token_context_mismatch", "usage", 2, "--page-token was created for a different profile, database cluster type, region, filter, or order-by expression")
	}
	if cursor.MatchedOffset == 0 && cursor.PageFingerprint != "" {
		return listCursor{}, invalidPageToken("page token contains an unexpected page fingerprint")
	}
	if cursor.MatchedOffset > 0 && cursor.PageFingerprint == "" {
		return listCursor{}, invalidPageToken("page token is missing its page fingerprint")
	}
	return cursor, nil
}

func encodeListCursor(cursor listCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", apperr.Wrap("db.invalid_page_token", "runtime", 1, "encode database cluster page token", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func pageFingerprint(clusters []apistarter.Cluster) string {
	h := sha256.New()
	for _, cluster := range clusters {
		_, _ = h.Write([]byte(cluster.ID))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func invalidPageToken(detail string) error {
	return apperr.New("db.invalid_page_token", "usage", 2, detail)
}

func normalizedProfileName(profile *config.Profile) string {
	if profile == nil || strings.TrimSpace(profile.Name) == "" {
		return "default"
	}
	return strings.TrimSpace(profile.Name)
}

func effectiveRegionCode(opts ListClustersOptions) string {
	if opts.Profile == nil {
		return ""
	}
	return strings.TrimSpace(opts.Profile.PlacementRegionCode)
}
