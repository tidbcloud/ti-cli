package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/tidbcloud/ti-cli/internal/api/transport"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
)

type Credentials struct {
	ProfileName string
	PublicKey   string
	PrivateKey  string
}

const tiDBCloudAPIKeysURL = "https://tidbcloud.com/org-settings/api-keys"

func LoadProfile(ctx context.Context, opts config.LoadOptions) (*config.Profile, error) {
	profile, err := config.Load(ctx, opts)
	if err == nil {
		if _, err := ValidateProfile(profile); err != nil {
			return nil, err
		}
		return profile, nil
	}

	var appErr *apperr.Error
	if errors.As(err, &appErr) {
		switch appErr.Code {
		case "config.missing_credentials":
			profileName := opts.Profile
			if profileName == "" {
				profileName = config.DefaultProfile
			}
			return nil, MissingCredentials(profileName, "tidb_cloud_public_key", "tidb_cloud_private_key")
		case "config.env_missing":
			if strings.Contains(appErr.Message, "TIDB_CLOUD_PUBLIC_KEY") || strings.Contains(appErr.Message, "TIDB_CLOUD_PRIVATE_KEY") {
				return nil, MissingEnvironmentCredentials()
			}
		}
	}

	return nil, err
}

func ValidateProfile(profile *config.Profile) (Credentials, error) {
	if profile == nil {
		return Credentials{}, MissingCredentials(config.DefaultProfile, "tidb_cloud_public_key", "tidb_cloud_private_key")
	}

	profileName := profile.Name
	if profileName == "" {
		profileName = config.DefaultProfile
	}

	missing := make([]string, 0, 2)
	if strings.TrimSpace(profile.TiDBCloudPublicKey) == "" {
		missing = append(missing, "tidb_cloud_public_key")
	}
	if strings.TrimSpace(profile.TiDBCloudPrivateKey) == "" {
		missing = append(missing, "tidb_cloud_private_key")
	}
	if len(missing) > 0 {
		return Credentials{}, MissingCredentials(profileName, missing...)
	}

	if malformedCredential(profile.TiDBCloudPublicKey) || strings.Contains(profile.TiDBCloudPublicKey, ":") {
		return Credentials{}, MalformedCredentials(profileName, "tidb_cloud_public_key")
	}
	if malformedCredential(profile.TiDBCloudPrivateKey) {
		return Credentials{}, MalformedCredentials(profileName, "tidb_cloud_private_key")
	}

	return Credentials{
		ProfileName: profileName,
		PublicKey:   profile.TiDBCloudPublicKey,
		PrivateKey:  profile.TiDBCloudPrivateKey,
	}, nil
}

// StatusError is the shared classification of a TiDB Cloud control-plane HTTP
// status that must keep ti's documented exit-code contract: 3 authentication,
// 4 authorization, 5 remote not found. Callers render it as their own error type
// (api.Error, apperr.Error) so the taxonomy has one source and cannot diverge.
type StatusError struct {
	Code     string
	Category string
	ExitCode int
	Message  string
}

// InvalidCredentials classifies HTTP 401 for a TiDB Cloud API key pair.
func InvalidCredentials(profileName string) StatusError {
	if profileName == "" {
		profileName = config.DefaultProfile
	}
	return StatusError{
		Code:     "auth.invalid_credentials",
		Category: "authentication",
		ExitCode: 3,
		Message:  fmt.Sprintf("authentication failed: TiDB Cloud rejected the API key pair for profile %q. Check ~/.ti/credentials or create a new API key.", profileName),
	}
}

// PermissionDenied classifies HTTP 403 for a TiDB Cloud control-plane request.
func PermissionDenied(profileName string, permission authz.Permission, action string) StatusError {
	err := authz.PermissionDenied(profileName, permission, action, "", "")
	return StatusError{
		Code:     apperr.CodeFor(err),
		Category: apperr.CategoryFor(err),
		ExitCode: apperr.ExitCodeFor(err),
		Message:  apperr.MessageFor(err),
	}
}

// NotFound classifies HTTP 404 for a TiDB Cloud control-plane request.
func NotFound(method, path string) StatusError {
	return StatusError{
		Code:     "api.not_found",
		Category: "api",
		ExitCode: 5,
		Message:  fmt.Sprintf("remote resource not found: %s %s", method, path),
	}
}

// AsError renders the classification as a CLI error.
func (e StatusError) AsError() error {
	return apperr.New(e.Code, e.Category, e.ExitCode, e.Message)
}

func NewDigestTransport(creds Credentials, base http.RoundTripper) http.RoundTripper {
	return transport.NewDigest(creds.PublicKey, creds.PrivateKey, base)
}

func MissingCredentials(profileName string, keys ...string) error {
	if profileName == "" {
		profileName = config.DefaultProfile
	}
	if len(keys) == 0 {
		keys = []string{"tidb_cloud_public_key", "tidb_cloud_private_key"}
	}
	return apperr.New(
		"auth.missing_credentials",
		"authentication",
		3,
		fmt.Sprintf(
			"authentication required: missing %s for profile %q. Run `ti configure` or set TIDB_CLOUD_PUBLIC_KEY and TIDB_CLOUD_PRIVATE_KEY. If you do not have a TiDB Cloud API key pair, generate one at %s.",
			joinKeys(keys),
			profileName,
			tiDBCloudAPIKeysURL,
		),
	)
}

func MissingEnvironmentCredentials() error {
	return apperr.New(
		"auth.missing_environment_credentials",
		"authentication",
		3,
		"authentication required: missing environment credentials. Set both TIDB_CLOUD_PUBLIC_KEY and TIDB_CLOUD_PRIVATE_KEY, or unset them to use profile credentials.",
	)
}

func MalformedCredentials(profileName, key string) error {
	if profileName == "" {
		profileName = config.DefaultProfile
	}
	return apperr.New(
		"auth.malformed_credentials",
		"authentication",
		3,
		fmt.Sprintf("authentication failed: malformed %s for profile %q. Check ~/.ti/credentials or create a new API key.", key, profileName),
	)
}

func malformedCredential(value string) bool {
	if value != strings.TrimSpace(value) {
		return true
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

func joinKeys(keys []string) string {
	if len(keys) == 1 {
		return keys[0]
	}
	if len(keys) == 2 {
		return keys[0] + " and " + keys[1]
	}
	return strings.Join(keys, ", ")
}
