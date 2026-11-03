package validate

import (
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

func TestStarterEndpointHostAcceptsTiDBCloudHosts(t *testing.T) {
	for _, tc := range []struct {
		host string
		want string
	}{
		{"gateway01.us-east-1.prod.aws.tidbcloud.com", "gateway01.us-east-1.prod.aws.tidbcloud.com"},
		{"gateway01.us-west-2.prod.aws.tidbcloud.com", "gateway01.us-west-2.prod.aws.tidbcloud.com"},
		{"gateway01-privatelink.us-east-1.prod.aws.tidbcloud.com", "gateway01-privatelink.us-east-1.prod.aws.tidbcloud.com"},
		{"gateway01.us-east-1.dev.shared.aws.tidbcloud.com", "gateway01.us-east-1.dev.shared.aws.tidbcloud.com"},
		{"gateway01.ap-southeast-1.prod.alicloud.tidbcloud.com", "gateway01.ap-southeast-1.prod.alicloud.tidbcloud.com"},
		{"x.gateway01.us-east-1.prod.aws.pg.tidbcloud.com", "x.gateway01.us-east-1.prod.aws.pg.tidbcloud.com"},
		{label63 + ".tidbcloud.com", label63 + ".tidbcloud.com"},
		{host253, host253},
		{"GATEWAY01.US-EAST-1.PROD.AWS.TIDBCLOUD.COM", "gateway01.us-east-1.prod.aws.tidbcloud.com"},
		{"gateway01.us-east-1.prod.aws.tidbcloud.com.", "gateway01.us-east-1.prod.aws.tidbcloud.com"},
		{" gateway01.us-east-1.prod.aws.tidbcloud.com ", "gateway01.us-east-1.prod.aws.tidbcloud.com"},
	} {
		got, err := StarterEndpointHost(tc.host)
		if err != nil {
			t.Fatalf("%q: unexpected error %v", tc.host, err)
		}
		if got != tc.want {
			t.Fatalf("%q: got %q, want %q", tc.host, got, tc.want)
		}
	}
}

func TestStarterEndpointHostRejectsOtherHosts(t *testing.T) {
	for _, host := range []string{
		"",
		"a@evil.example",
		"x@gateway01.us-east-1.prod.aws.tidbcloud.com",
		"evil.example/",
		"evil.example/.tidbcloud.com",
		"evil.example:443",
		"gateway01.us-east-1.prod.aws.tidbcloud.com:443",
		"evil.example?x.tidbcloud.com",
		"evil.example#.tidbcloud.com",
		"evil example.tidbcloud.com",
		"evil%2eexample.tidbcloud.com",
		`evil\example.tidbcloud.com`,
		"127.0.0.1",
		"[::1]",
		"evil.example",
		"tidbcloud.com",
		"eviltidbcloud.com",
		"tidbcloud.com.evil.example",
		"-gateway.tidbcloud.com",
		"gateway-.tidbcloud.com",
		"gateway..tidbcloud.com",
		"\u212Aelvin.tidbcloud.com",
		"gatew\u00e4y01.tidbcloud.com",
		label63 + "x.tidbcloud.com",
		"x" + host253,
	} {
		_, err := StarterEndpointHost(host)
		if apperr.CodeFor(err) != "db.unexpected_endpoint_host" {
			t.Fatalf("%q: expected db.unexpected_endpoint_host, got %v", host, err)
		}
	}
}

var (
	label63 = strings.Repeat("a", 63)
	// host253 is a 253-character name under tidbcloud.com, the DNS maximum.
	host253 = strings.Repeat(label63+".", 3) + strings.Repeat("b", 47) + ".tidbcloud.com"
)

func TestStarterEndpointHostLengthFixtures(t *testing.T) {
	if len(host253) != 253 {
		t.Fatalf("host253 has %d characters", len(host253))
	}
}

func TestStarterEndpointHostErrorEscapesAndCapsTheHost(t *testing.T) {
	_, err := StarterEndpointHost("\u212A" + strings.Repeat("a", 400))
	message := apperr.MessageFor(err)
	if !strings.Contains(message, `\u212a`) {
		t.Fatalf("non-ASCII host should be escaped in the message: %q", message)
	}
	if strings.Contains(message, strings.Repeat("a", 260)) {
		t.Fatalf("host should be capped in the message: %d bytes", len(message))
	}
}
