package connectionstring

import (
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/db/sqlcred"
)

func TestBuildFormats(t *testing.T) {
	input := Input{
		ClusterID:  "cluster-1",
		AccessMode: sqlcred.ReadWrite,
		Username:   "prefix.ti_rw",
		Password:   "pa:ss@word",
		Host:       "gateway01.us-east-1.prod.aws.tidbcloud.com",
		Port:       4000,
		Database:   "app db",
	}
	tests := map[string][]string{
		FormatMySQLURI:    {"mysql://prefix.ti_rw:pa%3Ass%40word@gateway01.us-east-1.prod.aws.tidbcloud.com:4000/app%20db?ssl-mode=VERIFY_IDENTITY"},
		FormatJDBC:        {"jdbc:mysql://gateway01.us-east-1.prod.aws.tidbcloud.com:4000/app%20db?", "user=prefix.ti_rw", "password=pa%3Ass%40word", "sslMode=VERIFY_IDENTITY"},
		FormatGoSQLDriver: {"prefix.ti_rw:pa\\:ss\\@word@tcp(gateway01.us-east-1.prod.aws.tidbcloud.com:4000)/app%20db?", "parseTime=true", "tls=true"},
		FormatSQLAlchemy:  {"mysql+pymysql://prefix.ti_rw:pa%3Ass%40word@gateway01.us-east-1.prod.aws.tidbcloud.com:4000/app%20db?ssl_verify_identity=true"},
	}
	for format, wants := range tests {
		t.Run(format, func(t *testing.T) {
			input.Format = format
			result, err := Build(input)
			if err != nil {
				t.Fatalf("Build failed: %v", err)
			}
			for _, want := range wants {
				if !strings.Contains(result.ConnectionString, want) {
					t.Fatalf("connection string %q did not contain %q", result.ConnectionString, want)
				}
			}
			if result.AccessMode != sqlcred.ReadWrite || result.TLSMode != TLSModeVerifyIdentity {
				t.Fatalf("unexpected result: %#v", result)
			}
		})
	}
}

func TestEnvFormatIncludesComponents(t *testing.T) {
	result, err := Build(Input{
		ClusterID:              "cluster-1",
		AccessMode:             sqlcred.ReadOnly,
		Username:               "prefix.ti_ro",
		Password:               "password with space",
		Host:                   "host.example.com",
		Port:                   4000,
		Database:               "test",
		Format:                 FormatEnv,
		EnvPrefix:              "APP_DB_",
		EnvIncludeDatabaseURL:  true,
		EnvDatabaseURLVariable: "APP_DATABASE_URL",
	})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	env := result.ConnectionString
	for _, want := range []string{
		"APP_DB_HOST=host.example.com\n",
		"APP_DB_PORT=4000\n",
		"APP_DB_USER=prefix.ti_ro\n",
		"APP_DB_PASSWORD=\"password with space\"\n",
		"APP_DB_ACCESS_MODE=read_only\n",
		"APP_DATABASE_URL=mysql://prefix.ti_ro:password%20with%20space@host.example.com:4000/test?ssl-mode=VERIFY_IDENTITY\n",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("env output missing %q:\n%s", want, env)
		}
	}
}

func TestInvalidFormatFails(t *testing.T) {
	_, err := Build(Input{Host: "host", Port: 4000, Format: "bad"})
	if err == nil {
		t.Fatal("expected invalid format to fail")
	}
}
