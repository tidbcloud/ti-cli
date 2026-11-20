package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// ErrInsecureURL reports that a transfer URL, or a redirect it led to, is not
// https. File bytes, export dumps, and release artifacts must not travel in
// cleartext, whatever URL a server hands back.
var ErrInsecureURL = errors.New("refused a transfer URL that is not https")

// ErrMalformedURL reports a transfer URL that does not parse. It never carries
// the URL itself: url.Parse and http.NewRequest repeat the whole raw URL in their
// errors, presigned signature included.
var ErrMalformedURL = errors.New("refused a malformed transfer URL")

// CheckSecureURL accepts https, and plain http only for localhost or a loopback
// IP (local test servers). It refuses user info in the URL. The error names only
// scheme://host, so a presigned signature in the query never reaches a message.
func CheckSecureURL(u *url.URL) error {
	if u == nil || u.Opaque != "" || u.Hostname() == "" {
		return fmt.Errorf("%w: missing host", ErrInsecureURL)
	}
	if u.User != nil {
		return fmt.Errorf("%w: %s carries user info", ErrInsecureURL, Origin(u))
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrInsecureURL, Origin(u))
}

// SecureRedirectThen returns a CheckRedirect policy that refuses any hop to a URL
// CheckSecureURL rejects, refuses any hop from https to plain http (loopback
// included, so a remote https server can't point a transfer at a local http
// service), keeps net/http's limit of MaxRedirects (a custom policy replaces it),
// and then defers to caller. A nil caller gets only these rules.
func SecureRedirectThen(caller func(req *http.Request, via []*http.Request) error) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= MaxRedirects {
			return ErrTooManyRedirects
		}
		if req == nil {
			return fmt.Errorf("%w: missing redirect target", ErrInsecureURL)
		}
		if err := CheckSecureURL(req.URL); err != nil {
			return err
		}
		if len(via) > 0 && via[len(via)-1].URL != nil &&
			strings.EqualFold(via[len(via)-1].URL.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
			return fmt.Errorf("%w: https redirect to %s", ErrInsecureURL, Origin(req.URL))
		}
		if caller != nil {
			return caller(req, via)
		}
		return nil
	}
}

// SecureTransferClient returns a copy of base (an empty client when nil) whose
// redirects must stay on https. Use it for transfers to server-chosen URLs such
// as presigned object-store URLs, which are on another origin by design.
func SecureTransferClient(base *http.Client) *http.Client {
	copied := http.Client{}
	if base != nil {
		copied = *base
	}
	copied.CheckRedirect = SecureRedirectThen(copied.CheckRedirect)
	return &copied
}

// NewSecureRequest builds a request for a server-chosen transfer URL. The URL
// must parse and pass CheckSecureURL before a request exists. A URL that does
// not parse is reported as ErrMalformedURL with the parser's reason only: the
// *url.Error's URL field repeats the raw URL, but its Err (for example
// `invalid URL escape "%zz"`) does not.
func NewSecureRequest(ctx context.Context, method, rawURL string, body io.Reader) (*http.Request, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMalformedURL, urlErr.Err)
		}
		return nil, ErrMalformedURL
	}
	if err := CheckSecureURL(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, ErrMalformedURL
	}
	return req, nil
}

// DoSecure checks req.URL with CheckSecureURL, then sends it through
// SecureTransferClient(client), so every redirect is held to the same rules
// whatever client the caller passes (a plain client, or nil, is fine).
// net/http wraps every failure in a *url.Error that repeats the full request URL,
// presigned signature included. A refused redirect comes back as the policy error
// alone; any other failure keeps its *url.Error with the URL's user info, query,
// and fragment removed. Build req with NewSecureRequest so building it can't leak
// the URL either.
func DoSecure(client *http.Client, req *http.Request) (*http.Response, error) {
	if err := CheckSecureURL(req.URL); err != nil {
		return nil, err
	}
	res, err := SecureTransferClient(client).Do(req)
	if err != nil {
		var urlErr *url.Error
		if !errors.As(err, &urlErr) {
			return nil, err
		}
		if errors.Is(err, ErrInsecureURL) || errors.Is(err, ErrTooManyRedirects) {
			return nil, urlErr.Err
		}
		if IsLocationParseError(urlErr.Err) {
			return nil, fmt.Errorf("%w: a redirect had an invalid Location header", ErrMalformedURL)
		}
		redacted := *urlErr
		redacted.URL = redactURL(urlErr.URL)
		return nil, &redacted
	}
	return res, nil
}

// IsLocationParseError reports whether err is net/http's failure to parse a
// redirect's Location header. net/http puts the raw Location value in that
// message (and does so before CheckRedirect runs), and a redirect to object
// storage carries a presigned signature there, so the error must not be shown.
func IsLocationParseError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "failed to parse Location header")
}

// redactURL drops the parts of a URL that can carry credentials: user info, the
// query (where presigned signatures live), and the fragment.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[redacted URL]"
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

// isLoopbackHost reports whether host is localhost or a loopback IP address.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
