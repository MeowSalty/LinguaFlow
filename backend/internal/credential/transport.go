package credential

import (
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
)

func NormalizeEndpoint(provider, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		switch provider {
		case "openai":
			raw = "https://api.openai.com/v1/"
		case "anthropic":
			raw = "https://api.anthropic.com/"
		case "google":
			raw = "https://generativelanguage.googleapis.com/"
		default:
			return "", ErrInvalid
		}
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", ErrEndpoint
	}
	if strings.Contains(u.Path, "\\") || strings.Contains(strings.ToLower(u.EscapedPath()), "%25") {
		return "", ErrEndpoint
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	u.Path = path.Clean("/"+u.Path) + "/"
	if u.Path == "//" {
		u.Path = "/"
	}
	u.RawPath = ""
	return u.String(), nil
}

func endpointContains(endpoint string, target *url.URL) bool {
	if target == nil || target.User != nil {
		return false
	}
	if strings.Contains(target.Path, "\\") || strings.Contains(strings.ToLower(target.EscapedPath()), "%25") {
		return false
	}
	base, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	normalized, err := NormalizeEndpoint("", target.Scheme+"://"+target.Host+target.EscapedPath())
	if err != nil {
		return false
	}
	u, err := url.Parse(normalized)
	if err != nil || u.Scheme != base.Scheme || u.Host != base.Host {
		return false
	}
	return strings.HasPrefix(u.Path, base.Path)
}

type guardedTransport struct {
	base               http.RoundTripper
	checker            Checker
	binding            Binding
	backendID          int
	provider, endpoint string
}

func (g *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !endpointContains(g.endpoint, req.URL) {
		return nil, ErrEndpoint
	}
	if err := g.checker.Check(req.Context(), g.binding, g.backendID, g.provider, g.endpoint); err != nil {
		return nil, err
	}
	return g.base.RoundTrip(req)
}

// GuardClient copies the client, preserving telemetry and timeout behavior.
// A transport-level guard covers every SDK attempt and redirected request.
func GuardClient(base *http.Client, checker Checker, binding Binding, backendID int, provider, endpoint string) (*http.Client, error) {
	if checker == nil || !binding.Valid() {
		return nil, ErrUnavailable
	}
	ep, err := NormalizeEndpoint(provider, endpoint)
	if err != nil {
		return nil, err
	}
	c := &http.Client{}
	if base != nil {
		*c = *base
	}
	transport := c.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	c.Transport = &guardedTransport{base: transport, checker: checker, binding: binding, backendID: backendID, provider: provider, endpoint: ep}
	prior := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !endpointContains(ep, req.URL) {
			return ErrEndpoint
		}
		if prior != nil {
			return prior(req, via)
		}
		if len(via) >= 10 {
			return ErrEndpoint
		}
		return nil
	}
	return c, nil
}
