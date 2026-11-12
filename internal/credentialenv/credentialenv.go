// Package credentialenv owns the single list of environment variables that
// carry ti credentials. Two processes are spawned with those credentials in
// scope: the ti-drive9 companion (which receives the ones it needs as DRIVE9_*
// values) and the untrusted child of `ti fs-vault run-with-secret` (which must
// receive none of them). Keeping one denylist here prevents the two scrub paths
// from drifting apart.
package credentialenv

import "strings"

// credentialPrefixes covers the credential families ti accepts: the TiDB Cloud
// API key pair, the legacy TDC_ names, the companion DRIVE9_ values, filesystem
// tokens, and filesystem vending/AI provider secrets. Non-credential TI_
// variables (TI_PROFILE, TI_REGION_CODE, TI_TEST_*, ...) are deliberately not
// matched: the companion still inherits the test controls it needs.
var credentialPrefixes = []string{
	"TIDB_CLOUD_",
	"TDC_",
	"DRIVE9_",
	"TI_FS_",
	"TI_VAULT_",
}

// IsCredential reports whether key names a ti credential environment variable.
//
// The match is case-insensitive because Windows environment variable names are:
// os.Environ() preserves the spelling a user set ("ti_fs_token"), while
// os.LookupEnv still resolves it to the credential value. Matching only the
// canonical spelling would hand that value to the child process.
func IsCredential(key string) bool {
	key = strings.ToUpper(key)
	for _, prefix := range credentialPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// Scrub returns a copy of env without credential variables. Entries that are not
// KEY=VALUE pairs are dropped, matching the mergeEnv consumer: they cannot name a
// credential and both spawn paths build a map keyed by name.
func Scrub(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if IsCredential(key) {
			continue
		}
		out = append(out, entry)
	}
	return out
}
