package authhttp

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authprotocol"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

type Transport struct {
	Deployment config.AuthDeploymentConfig
	Base       http.Transport
}

// RoundTrip validates the URL, obtains gate admission from the context, and
// holds the gate activity until the response body is fully read or closed.
// Gate errors are returned as-is so callers can identify blocked connections.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || !t.ValidURL(req.URL) {
		return nil, authprotocol.ErrUnavailable
	}

	req, release, gateErr := t.admit(req)
	if gateErr != nil {
		return nil, gateErr
	}

	resp, err := t.Base.RoundTrip(req)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, authprotocol.ErrUnavailable
	}

	var body io.ReadCloser = &limitedBody{ReadCloser: resp.Body, reader: io.LimitReader(resp.Body, t.Deployment.MaxResponseBytes+1), max: t.Deployment.MaxResponseBytes}
	if release != nil {
		body = &gatedBody{ReadCloser: body, release: release}
	}
	resp.Body = body
	return resp, nil
}

// admit checks the request context for gate info and calls Enter to register
// a cancellable activity. On success it returns a new request bound to the
// activity context and a release func; otherwise it returns the original
// request and a nil release (no gate), or the gate error itself.
func (t *Transport) admit(req *http.Request) (*http.Request, func(), error) {
	info := gateFromContext(req.Context())
	if info == nil || info.gate == nil || info.key == "" {
		return req, nil, nil
	}
	actCtx, release, err := info.gate.Enter(req.Context(), info.key, info.check)
	if err != nil {
		return req, nil, err
	}
	return req.WithContext(actCtx), release, nil
}

func (t *Transport) ValidRawURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && t.ValidURL(u)
}
func (t *Transport) ValidURL(u *url.URL) bool {
	return u != nil && u.IsAbs() && u.Host != "" && u.User == nil && u.Fragment == "" && (u.Scheme == "https" || (t.Deployment.AllowHTTPLoopback && u.Scheme == "http" && IsLoopbackHost(u.Hostname())))
}
func (t *Transport) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, authprotocol.ErrUnavailable
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, authprotocol.ErrUnavailable
	}
	var last error
	for _, ip := range ips {
		if !t.Allowed(host, ip.IP) {
			continue
		}
		conn, err := (&net.Dialer{Timeout: t.Deployment.RequestTimeout / 2}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	if last != nil {
		return nil, authprotocol.ErrUnavailable
	}
	return nil, authprotocol.ErrUnavailable
}
func (t *Transport) Allowed(host string, ip net.IP) bool {
	for _, h := range t.Deployment.AllowedHosts {
		if strings.EqualFold(strings.TrimSpace(h), host) {
			return true
		}
	}
	for _, raw := range t.Deployment.AllowedCIDRs {
		_, n, err := net.ParseCIDR(raw)
		if err == nil && n.Contains(ip) {
			return true
		}
	}
	if len(t.Deployment.AllowedHosts) > 0 || len(t.Deployment.AllowedCIDRs) > 0 {
		return false
	}
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}

type limitedBody struct {
	io.ReadCloser
	reader io.Reader
	max    int64
	read   int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	remaining := b.max - b.read
	if remaining < 0 {
		return 0, authprotocol.ErrUnavailable
	}
	limit := int64(len(p))
	if limit > remaining+1 {
		limit = remaining + 1
	}
	n, err := b.reader.Read(p[:limit])
	if int64(n) > remaining {
		allowed := int(remaining)
		b.read = b.max + 1
		return allowed, authprotocol.ErrUnavailable
	}
	b.read += int64(n)
	return n, err
}

func New(d config.AuthDeploymentConfig) *Transport {
	t := &Transport{Deployment: d, Base: http.Transport{ForceAttemptHTTP2: true, TLSHandshakeTimeout: d.RequestTimeout / 2}}
	t.Base.DialContext = t.Dial
	return t
}
