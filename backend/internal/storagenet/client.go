// Package storagenet 在拨号时强制执行部署的 S3 目标策略。
// 它绝不使用环境代理，也不允许对签名调用进行重定向。
package storagenet

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrEndpoint    = errors.New("invalid storage endpoint")
	ErrDestination = errors.New("storage destination is not allowed")
	ErrRedirect    = errors.New("storage redirects are not allowed")
)

// Policy 由部署提供，绝不来自 BYOS 请求。除绑定的 endpoint 外，
// Bucket 只允许那个确切的虚拟主机式 S3 桶。
type Policy struct {
	AllowedHosts    []string
	AllowedCIDRs    []string
	Bucket          string
	HeaderTimeout   time.Duration
	IdleTimeout     time.Duration
	TransferTimeout time.Duration
}

func NormalizeEndpoint(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", ErrEndpoint
	}
	// Endpoint 标识是一个 origin；对象路径由适配器提供。
	if u.Path != "" && u.Path != "/" {
		return "", ErrEndpoint
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if !validHost(host) {
		return "", ErrEndpoint
	}
	port := u.Port()
	if port != "" {
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return "", ErrEndpoint
		}
	}
	if port == "443" {
		port = ""
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	u.Path, u.RawPath = "", ""
	return u.String(), nil
}

func validHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Zone() == ""
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, part := range strings.Split(host, ".") {
		if len(part) == 0 || len(part) > 63 || part[0] == '-' || part[len(part)-1] == '-' {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

// ValidatePolicy 完全离线；config check/explain 可以安全调用它。
func ValidatePolicy(p Policy) error {
	for _, host := range p.AllowedHosts {
		if !validHost(strings.ToLower(host)) || strings.HasSuffix(host, ".") {
			return ErrDestination
		}
	}
	for _, cidr := range p.AllowedCIDRs {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return ErrDestination
		}
	}
	if p.HeaderTimeout < 0 || p.IdleTimeout < 0 || p.TransferTimeout < 0 {
		return ErrDestination
	}
	if p.Bucket != "" && (!validHost(p.Bucket) || strings.Contains(p.Bucket, ":")) {
		return ErrDestination
	}
	return nil
}

type resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}
type dialFunc func(context.Context, string, string) (net.Conn, error)

func NewClient(endpoint string, policy Policy) (*http.Client, error) {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return newClient(endpoint, policy, net.DefaultResolver, dialer.DialContext)
}

func newClient(endpoint string, p Policy, resolver resolver, dial dialFunc) (*http.Client, error) {
	endpoint, err := NormalizeEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if err = ValidatePolicy(p); err != nil {
		return nil, err
	}
	u, _ := url.Parse(endpoint)
	if p.HeaderTimeout == 0 {
		p.HeaderTimeout = 30 * time.Second
	}
	if p.IdleTimeout == 0 {
		p.IdleTimeout = 30 * time.Second
	}
	if p.TransferTimeout == 0 {
		p.TransferTimeout = 15 * time.Minute
	}
	allowed := map[string]bool{u.Hostname(): true}
	if p.Bucket != "" {
		if _, err := netip.ParseAddr(u.Hostname()); err == nil {
			return nil, ErrEndpoint
		}
		allowed[p.Bucket+"."+u.Hostname()] = true
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	transport := &http.Transport{
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   p.HeaderTimeout,
		ResponseHeaderTimeout: p.HeaderTimeout,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   4,
		ForceAttemptHTTP2:     false,
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, actualPort, err := net.SplitHostPort(address)
		if err != nil || actualPort != port || !allowed[strings.ToLower(host)] {
			return nil, ErrDestination
		}
		var ips []netip.Addr
		if ip, err := netip.ParseAddr(host); err == nil {
			ips = []netip.Addr{ip}
		} else {
			ips, err = resolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, ErrDestination
			}
		}
		if len(ips) == 0 {
			return nil, ErrDestination
		}
		// 在拨号任何地址前校验整个应答。只拨号这些
		// 数字地址，因此第二次 DNS 查询无法重新绑定目标。
		for _, ip := range ips {
			if !addressAllowed(host, ip, p) {
				return nil, ErrDestination
			}
		}
		for _, ip := range ips {
			conn, err := dial(ctx, "tcp", net.JoinHostPort(ip.Unmap().String(), port))
			if err == nil {
				return &idleConn{Conn: conn, timeout: p.IdleTimeout}, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		return nil, errors.New("storage destination connection failed")
	}
	return &http.Client{
		Transport:     &boundTransport{base: transport, allowed: allowed, port: port},
		Timeout:       p.TransferTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRedirect },
	}, nil
}

// idleConn 独立于整个请求的超时时间来限定读写停滞。
// HTTP/2 被禁用，因为其多路复用流共享一个 socket。
type idleConn struct {
	net.Conn
	timeout time.Duration
}

func (c *idleConn) Read(p []byte) (int, error) {
	if err := c.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}
func (c *idleConn) Write(p []byte) (int, error) {
	if err := c.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}

type boundTransport struct {
	base    *http.Transport
	allowed map[string]bool
	port    string
}

func (t *boundTransport) CloseIdleConnections() { t.base.CloseIdleConnections() }
func (t *boundTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || req.URL.User != nil || req.URL.Scheme != "https" || req.URL.Fragment != "" {
		return nil, ErrDestination
	}
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	if !t.allowed[strings.ToLower(req.URL.Hostname())] || port != t.port || (req.Host != "" && !strings.EqualFold(req.Host, req.URL.Host)) {
		return nil, ErrDestination
	}
	return t.base.RoundTrip(req)
}

var specialNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
}

func addressAllowed(host string, raw netip.Addr, p Policy) bool {
	ip := raw.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	// 即使有宽泛的允许列表，已知的云元数据服务仍被禁止。
	if ip == netip.MustParseAddr("169.254.169.254") || ip == netip.MustParseAddr("169.254.170.2") || ip == netip.MustParseAddr("100.100.100.200") || ip == netip.MustParseAddr("fd00:ec2::254") {
		return false
	}
	for _, name := range p.AllowedHosts {
		if strings.EqualFold(name, host) {
			return true
		}
	}
	for _, cidr := range p.AllowedCIDRs {
		prefix, err := netip.ParsePrefix(cidr)
		if err == nil && prefix.Contains(ip) {
			return true
		}
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range specialNetworks {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
