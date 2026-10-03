package storagenet

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"testing"
)

func TestNormalizeEndpointRejectsSecretAndInsecureOrigins(t *testing.T) {
	got, err := NormalizeEndpoint("https://S3.Example:443/")
	if err != nil || got != "https://s3.example" {
		t.Fatalf("normalize: %s %v", got, err)
	}
	for _, raw := range []string{"http://s3.example", "https://a:b@s3.example", "https://s3.example?secret=x", "https://s3.example?", "https://s3.example/a", "https://s3.example/#x", "https://s3.example:0", "https://s3.example:99999", "https://[fe80::1%25eth0]", "https://*.example"} {
		if _, err := NormalizeEndpoint(raw); err == nil {
			t.Fatalf("accepted endpoint %s", raw)
		}
	}
}

func TestAddressPolicy(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "172.16.2.3", "192.168.1.1", "169.254.169.254", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "100.100.100.200", "0.0.0.0", "192.0.2.1", "64:ff9b::7f00:1"} {
		if addressAllowed("storage.example", netip.MustParseAddr(raw), Policy{}) {
			t.Fatalf("private/special IP allowed: %s", raw)
		}
	}
	if !addressAllowed("storage.example", netip.MustParseAddr("8.8.8.8"), Policy{}) {
		t.Fatal("public IP blocked")
	}
	if !addressAllowed("storage.internal", netip.MustParseAddr("10.2.3.4"), Policy{AllowedHosts: []string{"storage.internal"}}) {
		t.Fatal("explicit internal host blocked")
	}
	if !addressAllowed("storage.internal", netip.MustParseAddr("10.2.3.4"), Policy{AllowedCIDRs: []string{"10.2.0.0/16"}}) {
		t.Fatal("explicit CIDR blocked")
	}
	if addressAllowed("metadata.internal", netip.MustParseAddr("169.254.169.254"), Policy{AllowedHosts: []string{"metadata.internal"}, AllowedCIDRs: []string{"0.0.0.0/0"}}) {
		t.Fatal("metadata allowlist bypass")
	}
}

type fakeResolver struct {
	ips   []netip.Addr
	calls int
}

func (r *fakeResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.calls++
	return r.ips, nil
}

func TestDialPinsResolvedIPAndRejectsMixedDNS(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		r := &fakeResolver{ips: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
		if mixed {
			r.ips = append(r.ips, netip.MustParseAddr("127.0.0.1"))
		}
		var addresses []string
		c, err := newClient("https://storage.example", Policy{}, r, func(_ context.Context, _ string, address string) (net.Conn, error) {
			addresses = append(addresses, address)
			return nil, errors.New("test dial")
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Get("https://storage.example/object")
		if err == nil {
			t.Fatal("expected failed request")
		}
		if mixed && len(addresses) != 0 {
			t.Fatal("dialed before validating entire DNS answer")
		}
		if !mixed && (len(addresses) != 1 || addresses[0] != "8.8.8.8:443" || r.calls != 1) {
			t.Fatalf("DNS was not pinned: %v calls %d", addresses, r.calls)
		}
	}
}

func TestOriginAndRedirectGuard(t *testing.T) {
	r := &fakeResolver{}
	c, err := newClient("https://storage.example", Policy{Bucket: "mybucket"}, r, func(context.Context, string, string) (net.Conn, error) { t.Fatal("unexpected dial"); return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"https://other.example/o", "http://storage.example/o", "https://otherbucket.storage.example/o", "https://storage.example:8443/o"} {
		if _, err := c.Get(target); !errors.Is(err, ErrDestination) {
			t.Fatalf("destination escaped binding: %v", err)
		}
	}
	if r.calls != 0 {
		t.Fatal("unbound target reached DNS")
	}
	if err := c.CheckRedirect(&http.Request{}, nil); !errors.Is(err, ErrRedirect) {
		t.Fatal("redirect allowed")
	}
	transport := c.Transport.(*boundTransport).base
	if transport.Proxy != nil || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("unsafe transport defaults")
	}
}
