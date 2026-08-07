package network

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/usestring/gqlcrawl/internal/model"
	"github.com/usestring/gqlcrawl/internal/source"
)

type Resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type ClassifiedError struct {
	Reason model.Reason
	Err    error
}

func (e *ClassifiedError) Error() string {
	return e.Err.Error()
}

func (e *ClassifiedError) Unwrap() error {
	return e.Err
}

func ReasonOf(err error) (model.Reason, bool) {
	var classified *ClassifiedError
	if errors.As(err, &classified) {
		return classified.Reason, true
	}
	return "", false
}

type Denylist struct {
	exact    map[string]struct{}
	suffixes []string
}

//go:embed denylist.txt
var bundledDenylist string

func LoadDenylist(path string) (*Denylist, error) {
	denylist := &Denylist{exact: make(map[string]struct{})}
	if err := denylist.add(strings.NewReader(bundledDenylist)); err != nil {
		return nil, fmt.Errorf("read bundled denylist: %w", err)
	}
	if path == "" {
		return denylist, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open denylist: %w", err)
	}
	defer file.Close()

	if err := denylist.add(file); err != nil {
		return nil, fmt.Errorf("read denylist: %w", err)
	}
	return denylist, nil
}

func (d *Denylist) add(reader io.Reader) error {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		entry := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(scanner.Text()), "."))
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		if strings.ContainsAny(entry, "/:@") {
			return fmt.Errorf("invalid denylist entry %q", entry)
		}
		if strings.HasPrefix(entry, "*.") {
			entry = strings.TrimPrefix(entry, "*")
		}
		if strings.HasPrefix(entry, ".") {
			d.suffixes = append(d.suffixes, entry)
			continue
		}
		d.exact[entry] = struct{}{}
	}
	return scanner.Err()
}

func (d *Denylist) Matches(host string) bool {
	if d == nil {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if _, found := d.exact[host]; found {
		return true
	}
	for _, suffix := range d.suffixes {
		if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
			return true
		}
	}
	return false
}

type Policy struct {
	allowHTTP bool
	denylist  *Denylist
	resolver  Resolver
}

func NewPolicy(allowHTTP bool, denylist *Denylist, resolver Resolver) *Policy {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &Policy{
		allowHTTP: allowHTTP,
		denylist:  denylist,
		resolver:  resolver,
	}
}

func (p *Policy) Validate(ctx context.Context, rawURL string) (*url.URL, []net.IP, error) {
	normalized, err := source.NormalizeURL(rawURL)
	if err != nil {
		return nil, nil, policyError(model.ReasonPolicyRejected, err)
	}

	parsed, err := url.Parse(normalized)
	if err != nil {
		return nil, nil, policyError(model.ReasonPolicyRejected, fmt.Errorf("parse normalized URL: %w", err))
	}
	if parsed.User != nil {
		return nil, nil, policyError(model.ReasonPolicyRejected, fmt.Errorf("URL userinfo is not allowed"))
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && p.allowHTTP) {
		return nil, nil, policyError(model.ReasonPolicyRejected, fmt.Errorf("URL scheme %q is not allowed", parsed.Scheme))
	}
	if port := parsed.Port(); port != "" {
		value, parseErr := strconv.Atoi(port)
		if parseErr != nil || value < 1 || value > 65535 {
			return nil, nil, policyError(model.ReasonPolicyRejected, fmt.Errorf("invalid port %q", port))
		}
	}

	host := strings.TrimSuffix(parsed.Hostname(), ".")
	if p.denylist.Matches(host) {
		return nil, nil, policyError(model.ReasonPolicyRejected, fmt.Errorf("host is denied by local policy"))
	}

	addresses, err := p.resolve(ctx, host)
	if err != nil {
		return nil, nil, err
	}
	return parsed, addresses, nil
}

func (p *Policy) resolve(ctx context.Context, host string) ([]net.IP, error) {
	if literal := net.ParseIP(host); literal != nil {
		if !isPublicIP(literal) {
			return nil, policyError(model.ReasonDNSNonPublic, fmt.Errorf("address is not public"))
		}
		return []net.IP{literal}, nil
	}

	resolved, err := p.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, policyError(model.ReasonPolicyRejected, fmt.Errorf("resolve host: %w", err))
	}
	if len(resolved) == 0 {
		return nil, policyError(model.ReasonPolicyRejected, fmt.Errorf("host resolved without addresses"))
	}

	addresses := make([]net.IP, 0, len(resolved))
	for _, result := range resolved {
		if !isPublicIP(result.IP) {
			return nil, policyError(model.ReasonDNSNonPublic, fmt.Errorf("host has a non-public DNS answer"))
		}
		addresses = append(addresses, append(net.IP(nil), result.IP...))
	}
	return addresses, nil
}

func policyError(reason model.Reason, err error) error {
	return &ClassifiedError{Reason: reason, Err: err}
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func isPublicIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}
