package transport

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/icholy/digest"
)

// MaxRedirects matches net/http's default limit, which a custom CheckRedirect
// replaces.
const MaxRedirects = 10

// ErrTooManyRedirects reports that a same-origin redirect chain hit MaxRedirects,
// so a caller can tell a loop from a refused cross-origin redirect.
var ErrTooManyRedirects = fmt.Errorf("stopped after %d redirects", MaxRedirects)

// SameOriginRedirect is the CheckRedirect policy for every credential-bearing
// client: it follows a redirect only when it keeps the scheme and host of the
// original request, because the digest and bearer transports attach credentials
// on every round trip (the digest transport answers whatever 401 challenge it
// receives, including one from a host the user never chose). A refused redirect
// comes back to the caller as the 3xx response.
func SameOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= MaxRedirects {
		return ErrTooManyRedirects
	}
	if req == nil || req.URL == nil || len(via) == 0 || via[0] == nil || via[0].URL == nil {
		return http.ErrUseLastResponse
	}
	if !SameOrigin(req.URL, via[0].URL) {
		return http.ErrUseLastResponse
	}
	return nil
}

// SameOriginThen returns a CheckRedirect policy that always applies the same-origin
// rule and then defers to caller. A caller-supplied policy can therefore narrow
// redirects further - for example refuse all of them - but can never re-enable a
// cross-origin hop for a credential-bearing client. A nil caller gets only the
// same-origin rule.
func SameOriginThen(caller func(req *http.Request, via []*http.Request) error) func(req *http.Request, via []*http.Request) error {
	if caller == nil {
		return SameOriginRedirect
	}
	return func(req *http.Request, via []*http.Request) error {
		if err := SameOriginRedirect(req, via); err != nil {
			return err
		}
		return caller(req, via)
	}
}

// CrossOriginRedirect classifies a 3xx response as a cross-origin redirect and
// returns the two origins as scheme://host, without userinfo, query, or fragment.
// It reports false for a non-redirect, a missing or unparsable Location, and a
// same-origin redirect - a caller that refused a same-origin redirect (for
// example through a stricter caller policy) must fall through to its normal
// status handling instead of reporting an origin change that did not happen.
func CrossOriginRedirect(res *http.Response, fallbackReq *http.Request) (fromOrigin, toOrigin string, ok bool) {
	if res == nil || res.StatusCode < 300 || res.StatusCode >= 400 {
		return "", "", false
	}
	req := res.Request
	if req == nil || req.URL == nil {
		req = fallbackReq
	}
	if req == nil || req.URL == nil {
		return "", "", false
	}
	// Resolve the header against the request URL directly: http.Response.Location
	// only resolves relative values when the response carries its own request, so a
	// fallback request would otherwise make "/other" look like another origin and
	// "//evil.example/other" lose its scheme.
	location := strings.TrimSpace(res.Header.Get("Location"))
	if location == "" {
		return "", "", false
	}
	target, err := req.URL.Parse(location)
	if err != nil || target == nil || SameOrigin(target, req.URL) {
		return "", "", false
	}
	fromOrigin, toOrigin = Origin(req.URL), Origin(target)
	if fromOrigin == "" || toOrigin == "" {
		// An originless target cannot name a different origin; never report one.
		return "", "", false
	}
	return fromOrigin, toOrigin, true
}

// Origin renders scheme://host for a URL, dropping any userinfo, path, query, or
// fragment. It returns an empty string when the URL has no origin.
func Origin(u *url.URL) string {
	if u == nil || u.Opaque != "" || u.Scheme == "" || u.Hostname() == "" {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}

// SameOrigin reports whether two URLs share a scheme and host. Host comparison
// folds case because DNS names are case-insensitive.
func SameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil || a.Opaque != "" || b.Opaque != "" {
		return false
	}
	if a.Scheme == "" || a.Hostname() == "" || b.Scheme == "" || b.Hostname() == "" {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

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
