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
