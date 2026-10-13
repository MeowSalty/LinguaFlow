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

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || !t.ValidURL(req.URL) {
		return nil, authprotocol.ErrUnavailable
	}
	resp, err := t.Base.RoundTrip(req)
	if err != nil {
		return nil, authprotocol.ErrUnavailable
	}
	resp.Body = &limitedBody{ReadCloser: resp.Body, reader: io.LimitReader(resp.Body, t.Deployment.MaxResponseBytes+1), max: t.Deployment.MaxResponseBytes}
	return resp, nil
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
