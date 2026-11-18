package transport

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// ErrInsecureURL reports that a transfer URL, or a redirect it led to, is not
// https. File bytes, export dumps, and release artifacts must not travel in
// cleartext, whatever URL a server hands back.
var ErrInsecureURL = errors.New("refused a transfer URL that is not https")

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
// CheckSecureURL rejects, keeps net/http's limit of MaxRedirects (a custom
// policy replaces it), and then defers to caller. A nil caller gets only the
// https rule and the limit.
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

// DoSecure checks req.URL with CheckSecureURL before sending it with client.
// net/http wraps every failure in a *url.Error that repeats the full request URL,
// presigned signature included. A refused redirect comes back as the policy error
// alone; any other failure keeps its *url.Error with the URL's user info, query,
// and fragment removed.
func DoSecure(client *http.Client, req *http.Request) (*http.Response, error) {
	if err := CheckSecureURL(req.URL); err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if !errors.As(err, &urlErr) {
			return nil, err
		}
		if errors.Is(err, ErrInsecureURL) || errors.Is(err, ErrTooManyRedirects) {
			return nil, urlErr.Err
		}
		redacted := *urlErr
		redacted.URL = redactURL(urlErr.URL)
		return nil, &redacted
	}
	return res, nil
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
