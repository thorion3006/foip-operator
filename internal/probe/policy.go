package probe

import (
	"context"
	"fmt"
	"net"
	"net/url"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

var blockedNetworks = []*net.IPNet{
	{IP: net.ParseIP("127.0.0.0"), Mask: net.CIDRMask(8, 32)},
	{IP: net.ParseIP("10.0.0.0"), Mask: net.CIDRMask(8, 32)},
	{IP: net.ParseIP("172.16.0.0"), Mask: net.CIDRMask(12, 32)},
	{IP: net.ParseIP("192.168.0.0"), Mask: net.CIDRMask(16, 32)},
	{IP: net.ParseIP("169.254.0.0"), Mask: net.CIDRMask(16, 32)},
	{IP: net.ParseIP("100.64.0.0"), Mask: net.CIDRMask(10, 32)},
	{IP: net.ParseIP("224.0.0.0"), Mask: net.CIDRMask(4, 32)},
	{IP: net.ParseIP("::1"), Mask: net.CIDRMask(128, 128)},
	{IP: net.ParseIP("fc00::"), Mask: net.CIDRMask(7, 128)},
	{IP: net.ParseIP("fe80::"), Mask: net.CIDRMask(10, 128)},
}

func validateTarget(ctx context.Context, target netcupv1.ProbeTarget, policy netcupv1.ProbeNetworkPolicy) error {
	if target.Address == "" {
		return fmt.Errorf("probe target address is empty")
	}
	lookupAddress, err := targetHostname(target.Address)
	if err != nil {
		return err
	}
	ips, err := allowedTargetIPs(ctx, lookupAddress, policy)
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return fmt.Errorf("probe target has no resolved addresses")
	}
	return nil
}

func targetHostname(address string) (string, error) {
	if parsed, err := url.Parse(address); err == nil && parsed.Scheme != "" {
		if parsed.User != nil {
			return "", fmt.Errorf("probe target userinfo is not allowed")
		}
		if parsed.Hostname() == "" {
			return "", fmt.Errorf("probe target URL has no host")
		}
		return parsed.Hostname(), nil
	}
	return address, nil
}

func allowedTargetIPs(ctx context.Context, address string, policy netcupv1.ProbeNetworkPolicy) ([]net.IP, error) {
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", address)
	if err != nil {
		if parsed := net.ParseIP(address); parsed != nil {
			ips = []net.IP{parsed}
		} else {
			return nil, fmt.Errorf("resolving probe target: %w", err)
		}
	}
	allowed, err := parseCIDRs(policy.AllowedCIDRs)
	if err != nil {
		return nil, err
	}
	denied, err := parseCIDRs(policy.DeniedCIDRs)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		for _, network := range denied {
			if network.Contains(ip) {
				return nil, fmt.Errorf("probe target resolves to denied network")
			}
		}
		if !policy.AllowPrivateNetworks && isBlocked(ip) && !containsIP(allowed, ip) {
			return nil, fmt.Errorf("probe target resolves to a sensitive network")
		}
	}
	return ips, nil
}

func dialAllowedContext(ctx context.Context, network, address string, policy netcupv1.ProbeNetworkPolicy) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid probe endpoint")
	}
	ips, err := allowedTargetIPs(ctx, host, policy)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{}
	var lastErr error
	for _, ip := range ips {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}

func parseCIDRs(values []string) ([]*net.IPNet, error) {
	result := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			return nil, fmt.Errorf("invalid probe CIDR %q", value)
		}
		result = append(result, network)
	}
	return result, nil
}

func isBlocked(ip net.IP) bool {
	for _, network := range blockedNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func containsIP(networks []*net.IPNet, ip net.IP) bool {
	for _, network := range networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
