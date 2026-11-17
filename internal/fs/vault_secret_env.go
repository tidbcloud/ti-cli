package fs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/credentialenv"
)

// normalizeVaultEnvKey converts a stored field name into an environment key
// exactly like the companion's env export does: it walks UTF-8 bytes, upper-cases
// ASCII letters, keeps digits (prefixing a leading one with '_'), keeps '_', and
// replaces every other byte with '_'. Walking bytes rather than runes matters for
// non-ASCII names ("café" exports as CAF__ because the accented letter is two
// bytes), so read-secret and run-with-secret agree on both the key and on whether
// two names collide.
func normalizeVaultEnvKey(field string) (string, error) {
	if field == "" {
		return "", apperr.New("vault.invalid_field", "usage", 2, "field name is required")
	}
	var b strings.Builder
	for i := 0; i < len(field); i++ {
		ch := field[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch == '_':
			b.WriteByte(ch)
		case ch >= 'a' && ch <= 'z':
			b.WriteByte(ch - 'a' + 'A')
		case ch >= '0' && ch <= '9':
			if i == 0 {
				b.WriteByte('_')
			}
			b.WriteByte(ch)
		default:
			b.WriteByte('_')
		}
	}
	return b.String(), nil
}

// normalizedVaultFieldMap converts field names to environment keys and rejects
// names that collide, so the export path and the injection path cannot diverge.
// Colliding names are reported in sorted order to keep the message stable across
// map iteration.
func normalizedVaultFieldMap(fields map[string]string) (map[string]string, error) {
	env := make(map[string]string, len(fields))
	owners := make(map[string]string, len(fields))
	for field, value := range fields {
		key, err := normalizeVaultEnvKey(field)
		if err != nil {
			return nil, err
		}
		if previous, exists := owners[key]; exists {
			first, second := previous, field
			if first > second {
				first, second = second, first
			}
			return nil, apperr.New("vault.env_collision", "usage", 2, fmt.Sprintf("secret fields %q and %q both normalize to %q", first, second, key))
		}
		owners[key] = field
		env[key] = value
	}
	return env, nil
}

func renderVaultEnv(fields map[string]string) (string, error) {
	env, err := normalizedVaultFieldMap(fields)
	if err != nil {
		return "", err
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		out.WriteString(key)
		out.WriteByte('=')
		out.WriteString(env[key])
		out.WriteByte('\n')
	}
	return out.String(), nil
}

// vaultEnvKeyDenylist names the environment variables that run-with-secret
// refuses to inject, mapped to the behavior they configure. A vault field is a
// secret value; these names are not values but process configuration: they decide
// which program runs (PATH, COMSPEC, SHELL), which code the dynamic loader or a
// language runtime pulls in (LD_PRELOAD, DYLD_INSERT_LIBRARIES, NODE_OPTIONS, ...),
// or which helper an ordinary tool executes (SSH_AUTH_SOCK, GIT_SSH_COMMAND,
// PAGER). Whoever can write a secret's fields could otherwise turn
// `ti fs-vault run-with-secret -- <command>` into arbitrary code execution inside
// the child without ever seeing ti's credentials.
//
// Keys are compared in the normalized form produced by normalizeVaultEnvKey,
// which upper-cases ASCII, so "path", "Path", and "PATH" all hit PATH and case
// cannot be used to slip a protected name past the check.
//
// The list is exact-name rather than prefix based on purpose: names such as
// SSH_PRIVATE_KEY, GIT_TOKEN, or LD_PRELOAD_BACKUP are plausible secret values
// that stay injectable, and only the exact process-configuration names are
// rejected. Bash's exported-function form ("BASH_FUNC_name%%") does not need an
// entry: the normalizer rewrites every byte outside A-Z, a-z, 0-9, and '_' to
// '_', so a field name can never reach the child in that shape.
//
// Two escape hatches in the exact-name shape are closed by vaultEnvKeyDenied: a
// matching name is matched again after any trailing "version segment" is stripped
// (Lua reads LUA_INIT_5_4, LUA_PATH_5_4, and LUA_CPATH_5_4 before the unsuffixed
// names, and the Solaris loader reads LD_PRELOAD_32), and a few families whose
// suffix the field name chooses are matched by prefix (the whole DYLD_ namespace,
// LD_32_/LD_64_, and Git's GIT_CONFIG_KEY_<n>/GIT_CONFIG_VALUE_<n>).
//
// The gate lives on the injection path instead of create-secret so stored secrets
// stay readable: `read-secret --format env` only prints values and never spawns a
// child, and an existing secret with such a field remains inspectable.
var vaultEnvKeyDenylist = map[string]string{
	// Which program runs.
	"PATH":     "the child's command lookup",
	"PATHEXT":  "the child's command lookup",
	"COMSPEC":  "the child's command interpreter",
	"SHELL":    "the shell a child tool starts",
	"ENV":      "shell startup",
	"BASH_ENV": "shell startup",
	"ZDOTDIR":  "shell startup",
	"KSH_ENV":  "shell startup",
	// Shell evaluations and word splitting.
	"SHELLOPTS":  "shell option handling",
	"BASHOPTS":   "shell option handling",
	"PS4":        "shell tracing",
	"IFS":        "shell word splitting",
	"CDPATH":     "shell directory lookup",
	"GLOBIGNORE": "shell globbing",
	// Dynamic loader and the data files it loads.
	"LD_PRELOAD":                   "the dynamic loader",
	"LD_LIBRARY_PATH":              "the dynamic loader",
	"LD_AUDIT":                     "the dynamic loader",
	"LD_DEBUG":                     "the dynamic loader",
	"LD_PROFILE":                   "the dynamic loader",
	"GCONV_PATH":                   "character set conversion",
	"GLIBC_TUNABLES":               "the C library",
	"LD_ORIGIN_PATH":               "the dynamic loader",
	"LOCPATH":                      "locale data loading",
	"NLSPATH":                      "message catalog loading",
	"DYLD_INSERT_LIBRARIES":        "the dynamic loader",
	"DYLD_LIBRARY_PATH":            "the dynamic loader",
	"DYLD_FRAMEWORK_PATH":          "the dynamic loader",
	"DYLD_FALLBACK_LIBRARY_PATH":   "the dynamic loader",
	"DYLD_FALLBACK_FRAMEWORK_PATH": "the dynamic loader",
	"DYLD_IMAGE_SUFFIX":            "the dynamic loader",
	// Language runtimes that load code named by the environment.
	"PYTHONPATH":            "Python module lookup",
	"PYTHONHOME":            "Python module lookup",
	"PYTHONSTARTUP":         "Python startup",
	"PERL5LIB":              "Perl module lookup",
	"PERL5OPT":              "Perl startup",
	"PERL5SHELL":            "the shell Perl starts",
	"PERLLIB":               "Perl module lookup",
	"PYTHONBREAKPOINT":      "the Python breakpoint hook",
	"PYTHONUSERBASE":        "Python user site loading",
	"NODE_OPTIONS":          "Node.js startup",
	"NODE_PATH":             "Node.js module lookup",
	"RUBYOPT":               "Ruby startup",
	"RUBYLIB":               "Ruby module lookup",
	"JAVA_TOOL_OPTIONS":     "Java startup",
	"JDK_JAVA_OPTIONS":      "Java startup",
	"IBM_JAVA_OPTIONS":      "Java startup",
	"OPENJ9_JAVA_OPTIONS":   "Java startup",
	"_JAVA_OPTIONS":         "Java startup",
	"JAVA_OPTIONS":          "Java startup",
	"CLASSPATH":             "Java class loading",
	"LUA_INIT":              "Lua startup",
	"LUA_PATH":              "Lua module lookup",
	"LUA_CPATH":             "Lua module lookup",
	"PHPRC":                 "PHP configuration loading",
	"PHP_INI_SCAN_DIR":      "PHP configuration loading",
	"GEM_HOME":              "Ruby gem lookup",
	"GEM_PATH":              "Ruby gem lookup",
	"RUBYGEMS_GEMDEPS":      "Ruby gem autoloading",
	"DOTNET_STARTUP_HOOKS":  "the .NET startup hook",
	"CORECLR_PROFILER":      "the .NET profiler",
	"CORECLR_PROFILER_PATH": "the .NET profiler",
	"COR_PROFILER":          "the .NET profiler",
	"COR_PROFILER_PATH":     "the .NET profiler",
	// Helpers that a tool executes on the child's behalf.
	"SSH_AUTH_SOCK":         "the SSH agent",
	"SSH_ASKPASS":           "SSH program selection",
	"SUDO_ASKPASS":          "sudo program selection",
	"GIT_SSH":               "Git transport program selection",
	"GIT_SSH_COMMAND":       "Git transport program selection",
	"GIT_PROXY_COMMAND":     "Git transport program selection",
	"GIT_EXEC_PATH":         "Git subcommand lookup",
	"GIT_CONFIG":            "Git configuration loading",
	"GIT_CONFIG_GLOBAL":     "Git configuration loading",
	"GIT_CONFIG_SYSTEM":     "Git configuration loading",
	"GIT_CONFIG_COUNT":      "Git configuration loading",
	"GIT_CONFIG_PARAMETERS": "Git configuration loading",
	"GIT_DIR":               "Git repository selection",
	"GIT_COMMON_DIR":        "Git repository selection",
	"GIT_WORK_TREE":         "Git work tree selection",
	"GIT_EXTERNAL_DIFF":     "the Git diff helper",
	"GIT_ASKPASS":           "Git credential prompting",
	"GIT_TEMPLATE_DIR":      "Git hook installation",
	"GIT_PAGER":             "the pager Git starts",
	"GIT_EDITOR":            "the editor Git starts",
	"GIT_SEQUENCE_EDITOR":   "the editor Git starts",
	"PAGER":                 "the pager a child tool starts",
	"MANPAGER":              "the pager a child tool starts",
	"EDITOR":                "the editor a child tool starts",
	"VISUAL":                "the editor a child tool starts",
	"LESSOPEN":              "the less input preprocessor",
	"LESSCLOSE":             "the less input preprocessor",
	// Shell function/module search and TLS provider loading.
	"FPATH":           "shell function lookup",
	"OPENSSL_CONF":    "OpenSSL configuration loading",
	"OPENSSL_ENGINES": "OpenSSL engine loading",
	"OPENSSL_MODULES": "OpenSSL provider loading",
	// Windows shell and loader configuration.
	"PSMODULEPATH": "PowerShell module loading",
	"SYSTEMROOT":   "the Windows system directory",
	"WINDIR":       "the Windows system directory",
}

// validateVaultEnvFields maps secret field names to child environment keys using
// the same normalization as `read-secret --format env`, so a secret that command
// accepts is also injectable here. Field names that normalize to the same key are
// rejected instead of silently overwriting each other, and names that configure
// how a child resolves or loads code are rejected because injecting them would let
// the secret's author choose what the child executes.
func validateVaultEnvFields(fields map[string]string) (map[string]string, error) {
	env, err := normalizedVaultFieldMap(fields)
	if err != nil {
		return nil, err
	}
	// Sorted keys keep the error deterministic, and every rejected name is
	// collected so one error lists the whole set the user has to rename.
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rejected := make([]string, 0, len(keys))
	for _, key := range keys {
		if controls, blocked := vaultEnvKeyDenied(key); blocked {
			rejected = append(rejected, fmt.Sprintf("%q (%s)", key, controls))
		}
	}
	if len(rejected) > 0 {
		return nil, apperr.New("vault.env_key_forbidden", "usage", 2, fmt.Sprintf("secret fields map to environment variables that configure the child process: %s; run-with-secret injects secret values only, so rename those fields", strings.Join(rejected, ", ")))
	}
	for _, key := range keys {
		value := env[key]
		if idx := indexOfForbiddenVaultEnvByte(value); idx >= 0 {
			return nil, apperr.New("vault.invalid_env_value", "usage", 2, fmt.Sprintf("secret value for key %q contains forbidden control byte 0x%02x at offset %d", key, value[idx], idx))
		}
	}
	return env, nil
}

// vaultEnvKeyDenied reports the behavior a normalized child environment key would
// configure, and whether run-with-secret must refuse it. Each candidate name - the
// key itself, then the key with trailing version segments removed - is matched
// against the exact-name table and the prefix families.
//
// The version-stripped candidate exists because a runtime can read a versioned
// variant in preference to the documented base name: Lua's standalone interpreter
// consults LUA_INIT_5_4, LUA_PATH_5_4, and LUA_CPATH_5_4 before LUA_INIT, LUA_PATH,
// and LUA_CPATH for the matching release, and the Solaris loader reads
// LD_PRELOAD_32/LD_PRELOAD_64, so an exact-name table alone would let those through.
func vaultEnvKeyDenied(key string) (string, bool) {
	candidates := []string{key}
	if base, versioned := trimVaultEnvVersionSuffix(key); versioned {
		candidates = append(candidates, base)
	}
	for _, candidate := range candidates {
		if controls, blocked := vaultEnvKeyDenylist[candidate]; blocked {
			return controls, true
		}
		for _, family := range vaultEnvKeyFamilies {
			if strings.HasPrefix(candidate, family.prefix) {
				return family.controls, true
			}
		}
	}
	return "", false
}

// vaultEnvKeyFamilies covers the families whose exact spelling cannot be listed:
// the dynamic loader's whole DYLD_ namespace, the 32/64-bit loader variants, and
// Git's indexed configuration entries, where the suffix is chosen by whoever names
// the field. Everything else stays in the exact-name table so plausible secret
// names keep working.
var vaultEnvKeyFamilies = []struct {
	prefix   string
	controls string
}{
	{"DYLD_", "the dynamic loader"},
	{"LD_32_", "the dynamic loader"},
	{"LD_64_", "the dynamic loader"},
	{"GIT_CONFIG_KEY_", "Git configuration loading"},
	{"GIT_CONFIG_VALUE_", "Git configuration loading"},
}

// trimVaultEnvVersionSuffix strips trailing all-digit segments, so LUA_INIT_5_4
// becomes LUA_INIT and LUA_PATH_5_1 becomes LUA_PATH. A name without such a
// suffix is returned unchanged with versioned=false, and the loop stops before the
// name can be reduced to nothing.
func trimVaultEnvVersionSuffix(key string) (string, bool) {
	base := key
	for {
		idx := strings.LastIndexByte(base, '_')
		if idx <= 0 || !isVaultEnvVersionSegment(base[idx+1:]) {
			break
		}
		base = base[:idx]
	}
	return base, base != key
}

func isVaultEnvVersionSegment(segment string) bool {
	if segment == "" {
		return false
	}
	for i := 0; i < len(segment); i++ {
		if segment[i] < '0' || segment[i] > '9' {
			return false
		}
	}
	return true
}

func indexOfForbiddenVaultEnvByte(value string) int {
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b < 0x20 && b != '\t' {
			return i
		}
	}
	return -1
}

func mergeEnv(base []string, overrides map[string]string) []string {
	merged := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			merged[key] = value
		}
	}
	for key, value := range overrides {
		merged[key] = value
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+merged[key])
	}
	return out
}

// runVaultSecretCommand runs the user command with the vault fields injected into
// an environment scrubbed of ti's credentials. It is the only place a
// run-with-secret child is spawned, and it never receives the filesystem bearer
// token or the vault token.
func runVaultSecretCommand(ctx context.Context, opts VaultRunWithSecretOptions, fields map[string]string) error {
	env, err := vaultSecretChildEnv(os.Environ(), fields)
	if err != nil {
		return err
	}
	command := append([]string(nil), opts.Command...)
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Env = env
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	if err := cmd.Run(); err != nil {
		exitCode := 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
			exitCode = exitErr.ExitCode()
		}
		return apperr.Wrap("vault.command_failed", "runtime", exitCode, fmt.Sprintf("vault command %q failed", command[0]), err)
	}
	return nil
}

// vaultSecretChildEnv builds the child environment: every ti credential family
// (TIDB_CLOUD_*, TDC_*, DRIVE9_*, TI_FS_*, TI_VAULT_*) is removed by the shared
// credentialenv denylist, then the validated vault fields are added. Secret
// fields win over any inherited variable with the same name.
func vaultSecretChildEnv(base []string, fields map[string]string) ([]string, error) {
	secretEnv, err := validateVaultEnvFields(fields)
	if err != nil {
		return nil, err
	}
	return mergeEnv(dropShadowedEnvKeys(credentialenv.Scrub(base), secretEnv), secretEnv), nil
}

// envKeysFoldCase reports whether environment names compare case-insensitively
// on this platform. Windows resolves them that way and keeps the last duplicate
// when exec builds the block, so an ambient db_url would otherwise shadow the
// injected DB_URL. Unix keeps case-distinct names (for example http_proxy next to
// HTTP_PROXY), so the deduplication must not fold there.
var envKeysFoldCase = runtime.GOOS == "windows"

// dropShadowedEnvKeys removes inherited entries that would be overridden by an
// injected secret field under the target platform's name equality.
func dropShadowedEnvKeys(base []string, overrides map[string]string) []string {
	out := make([]string, 0, len(base))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if ok && envKeyShadowed(key, overrides) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func envKeyShadowed(key string, overrides map[string]string) bool {
	for override := range overrides {
		if envKeysEqual(key, override) {
			return true
		}
	}
	return false
}

func envKeysEqual(a, b string) bool {
	if envKeysFoldCase {
		return strings.EqualFold(a, b)
	}
	return a == b
}
