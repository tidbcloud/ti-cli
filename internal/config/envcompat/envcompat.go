package envcompat

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

type Pair struct {
	Canonical string
	Legacy    string
}

var PublicPairs = []Pair{
	{Canonical: "TI_PROFILE", Legacy: "TDC_PROFILE"},
	{Canonical: "TI_REGION_CODE", Legacy: "TDC_REGION_CODE"},
	{Canonical: "TIDB_CLOUD_PUBLIC_KEY", Legacy: "TDC_PUBLIC_KEY"},
	{Canonical: "TIDB_CLOUD_PRIVATE_KEY", Legacy: "TDC_PRIVATE_KEY"},
	{Canonical: "TI_FS_TOKEN", Legacy: "TDC_FS_TOKEN"},
	{Canonical: "TI_FS_FILE_SYSTEM_ID", Legacy: "TDC_FS_FILE_SYSTEM_ID"},
	{Canonical: "TI_LOGGING", Legacy: "TDC_LOGGING"},
	{Canonical: "TI_TELEMETRY", Legacy: "TDC_TELEMETRY"},
	{Canonical: "TI_TELEMETRY_TAG", Legacy: "TDC_TELEMETRY_TAG"},
	{Canonical: "TI_TELEMETRY_EXTRA", Legacy: "TDC_TELEMETRY_EXTRA"},
	{Canonical: "TI_VAULT_TOKEN", Legacy: "TDC_VAULT_TOKEN"},
	{Canonical: "TI_INSTALL_DIR", Legacy: "TDC_INSTALL_DIR"},
}

func Resolve(env map[string]string, pair Pair) (value string, present bool, usedLegacy bool, err error) {
	canonicalValue, canonicalSet := lookup(env, pair.Canonical)
	legacyValue, legacySet := lookup(env, pair.Legacy)
	if canonicalSet && legacySet && canonicalValue != legacyValue {
		return "", false, false, apperr.New(
			"config.environment_conflict",
			"config",
			2,
			fmt.Sprintf("environment variables %s and %s contain different values; unset one or make them equal", pair.Canonical, pair.Legacy),
		)
	}
	if canonicalSet {
		return canonicalValue, true, false, nil
	}
	if legacySet {
		return legacyValue, true, true, nil
	}
	return "", false, false, nil
}

func ResolveNames(env map[string]string, canonical, legacy string) (string, bool, bool, error) {
	return Resolve(env, Pair{Canonical: canonical, Legacy: legacy})
}

func LegacyNames(env map[string]string) []string {
	names := make([]string, 0)
	for _, pair := range PublicPairs {
		if _, ok := lookup(env, pair.Legacy); ok {
			names = append(names, pair.Legacy)
		}
	}
	sort.Strings(names)
	return names
}

func Validate(env map[string]string) error {
	for _, pair := range PublicPairs {
		if _, _, _, err := Resolve(env, pair); err != nil {
			return err
		}
	}
	return nil
}

func LegacyNameFor(canonical string) string {
	for _, pair := range PublicPairs {
		if pair.Canonical == canonical {
			return pair.Legacy
		}
	}
	return strings.Replace(canonical, "TI_", "TDC_", 1)
}

func lookup(env map[string]string, key string) (string, bool) {
	if env != nil {
		value, ok := env[key]
		return value, ok
	}
	return os.LookupEnv(key)
}
