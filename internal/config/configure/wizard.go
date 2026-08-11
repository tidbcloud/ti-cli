package configure

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/config/envcompat"
	"github.com/tidbcloud/ti-cli/internal/config/region"
	"github.com/tidbcloud/ti-cli/internal/config/store"
	"github.com/tidbcloud/ti-cli/internal/secretinput"
)

type Options struct {
	Profile             string
	HomeDir             string
	RegionCode          string
	TiDBCloudPublicKey  string
	TiDBCloudPrivateKey string
	NonInteractive      bool
	Env                 map[string]string
	In                  io.Reader
	Out                 io.Writer
}

type Result struct {
	Profile           string `json:"profile"`
	RegionCode        string `json:"region_code"`
	CredentialsStored bool   `json:"credentials_stored"`
}

func (r Result) Human() string {
	return fmt.Sprintf("Profile: %s\nRegion: %s\nCredentials stored: %t", r.Profile, r.RegionCode, r.CredentialsStored)
}

func Run(ctx context.Context, opts Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if opts.Profile == "" {
		opts.Profile = config.DefaultProfile
	}
	if err := config.ValidateProfileName(opts.Profile); err != nil {
		return Result{}, err
	}
	if opts.HomeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Result{}, apperr.Wrap("config.home_dir", "config", 1, "cannot determine home directory", err)
		}
		opts.HomeDir = home
	}
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	input := opts.In
	if !secretinput.IsTerminal(opts.In) {
		input = bufio.NewReader(opts.In)
	}

	defaultRegion := region.DefaultPlacementCode()
	regionInput, err := valueOrEnv(opts.RegionCode, opts.Env, "TI_REGION_CODE")
	if err != nil {
		return Result{}, err
	}
	regionCode, err := valueOrPrompt(ctx, input, opts.Out, regionInput, "default region code", fmt.Sprintf("Default region code (%s): ", defaultRegion), defaultRegion, false, opts.NonInteractive)
	if err != nil {
		return Result{}, err
	}
	placement, err := region.ParsePlacementCode(regionCode)
	if err != nil {
		return Result{}, apperr.Wrap("config.invalid_region", "config", 2, err.Error(), err)
	}

	publicKeyInput, err := valueOrEnv(opts.TiDBCloudPublicKey, opts.Env, "TIDB_CLOUD_PUBLIC_KEY")
	if err != nil {
		return Result{}, err
	}
	publicKey, err := valueOrPrompt(ctx, input, opts.Out, publicKeyInput, "TiDB Cloud public key", "TiDB Cloud public key: ", "", false, opts.NonInteractive)
	if err != nil {
		return Result{}, err
	}
	privateKeyInput, err := valueOrEnv(opts.TiDBCloudPrivateKey, opts.Env, "TIDB_CLOUD_PRIVATE_KEY")
	if err != nil {
		return Result{}, err
	}
	privateKey, err := valueOrPrompt(ctx, input, opts.Out, privateKeyInput, "TiDB Cloud private key", "TiDB Cloud private key: ", "", true, opts.NonInteractive)
	if err != nil {
		return Result{}, err
	}

	if err := store.WriteConfiguredProfile(opts.HomeDir, opts.Profile, store.ConfigProfile{
		RegionCode: placement.Code,
	}, store.CredentialsProfile{
		TiDBCloudPublicKey:  publicKey,
		TiDBCloudPrivateKey: privateKey,
	}); err != nil {
		return Result{}, apperr.Wrap("config.write_failed", "config", 1, err.Error(), err)
	}

	return Result{
		Profile:           opts.Profile,
		RegionCode:        placement.Code,
		CredentialsStored: true,
	}, nil
}

func valueOrPrompt(ctx context.Context, in io.Reader, out io.Writer, value, fieldName, prompt, defaultValue string, secret, nonInteractive bool) (string, error) {
	if value != "" {
		return strings.TrimSpace(value), nil
	}
	if nonInteractive {
		return "", apperr.New(
			"config.non_interactive_missing",
			"config",
			2,
			fmt.Sprintf("%s is required for non-interactive configure; provide a flag or TI_* environment variable", fieldName),
		)
	}

	value, err := secretinput.Read(ctx, prompt, in, out, secret)
	if err != nil {
		return "", err
	}
	if value == "" {
		if defaultValue != "" {
			return defaultValue, nil
		}
		return "", apperr.New("config.required_input", "config", 2, fieldName+" is required")
	}
	return strings.TrimSpace(value), nil
}

func valueOrEnv(value string, env map[string]string, key string) (string, error) {
	if value != "" {
		return value, nil
	}
	resolved, _, _, err := envcompat.ResolveNames(env, key, envcompat.LegacyNameFor(key))
	return resolved, err
}
