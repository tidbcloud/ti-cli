package transport

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/icholy/digest"
)

func NewDigest(username, password string, base http.RoundTripper) http.RoundTripper {
	return &digest.Transport{
		Username:  username,
		Password:  password,
		Transport: base,
	}
}

func NewBearer(apiKey string, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return bearerTransport{apiKey: apiKey, base: base}
}

type bearerTransport struct {
	apiKey string
	base   http.RoundTripper
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.apiKey)
	return t.base.RoundTrip(clone)
}

type Redactor struct {
	Secrets []string
}

func (r Redactor) Redact(value string) string {
	out := value
	for _, secret := range r.Secrets {
		if secret == "" {
			continue
		}
		out = strings.ReplaceAll(out, secret, "[REDACTED]")
	}
	return out
}

func NewDebugRoundTripper(base http.RoundTripper, writer io.Writer, redactor Redactor) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if writer == nil {
		return base
	}
	return debugRoundTripper{
		base:     base,
		writer:   writer,
		redactor: redactor,
	}
}

type debugRoundTripper struct {
	base     http.RoundTripper
	writer   io.Writer
	redactor Redactor
}

func (t debugRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	fmt.Fprintf(t.writer, "ti debug: request %s %s\n", req.Method, t.redactor.Redact(req.URL.String()))
	res, err := t.base.RoundTrip(req)
	if err != nil {
		fmt.Fprintf(t.writer, "ti debug: error %s\n", t.redactor.Redact(err.Error()))
		return nil, err
	}
	fmt.Fprintf(t.writer, "ti debug: response %d %s\n", res.StatusCode, http.StatusText(res.StatusCode))
	return res, nil
}
