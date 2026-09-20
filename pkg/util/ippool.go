package util

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"net"
	"sort"
	"strings"

	"k8s.io/utils/set"
)

func ExpandIPPoolAddressesForOVN(entries []string) ([]string, error) {
	addresses, err := expandSingleFamilyIPPoolAddresses(entries)
	if err != nil {
		return nil, err
	}

	for i, addr := range addresses {
		addresses[i] = simplifyOVNAddress(addr)
	}
	return addresses, nil
}

func expandSingleFamilyIPPoolAddresses(entries []string) ([]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}

	seen := make(map[string]struct{})
	hasIPv4 := false
	hasIPv6 := false

	addUnique := func(cidr string) {
		if _, exists := seen[cidr]; !exists {
			seen[cidr] = struct{}{}
			if strings.Contains(cidr, ":") {
				hasIPv6 = true
			} else {
				hasIPv4 = true
			}
		}
	}

	for _, raw := range entries {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}

		switch {
		case strings.Contains(value, ".."):
			cidrs, err := expandIPRange(value)
			if err != nil {
				return nil, err
			}
			for _, cidr := range cidrs {
				addUnique(cidr)
			}
		case strings.Contains(value, "/"):
			cidr, err := normalizeCIDR(value)
			if err != nil {
				return nil, err
			}
			addUnique(cidr)
		default:
			cidr, err := ipToCIDR(value)
			if err != nil {
				return nil, err
			}
			addUnique(cidr)
		}
	}

	if hasIPv4 && hasIPv6 {
		return nil, errors.New("mixed IPv4 and IPv6 addresses are not supported in OVN address set")
	}

	addresses := make([]string, 0, len(seen))
	for cidr := range seen {
		addresses = append(addresses, cidr)
	}
	sort.Strings(addresses)
	return addresses, nil
}

func expandIPRange(value string) ([]string, error) {
	parts := strings.Split(value, "..")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid IP range %q", value)
	}

	start, err := NormalizeIP(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid range start %q: %w", parts[0], err)
	}
	end, err := NormalizeIP(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid range end %q: %w", parts[1], err)
	}
	if (start.To4() != nil) != (end.To4() != nil) {
		return nil, fmt.Errorf("range %q mixes IPv4 and IPv6 addresses", value)
	}
	if compareIP(start, end) > 0 {
		return nil, fmt.Errorf("range %q start is greater than end", value)
	}

	cidrs, err := IPRangeToCIDRs(start, end)
	if err != nil {
		return nil, fmt.Errorf("failed to convert IP range %q to CIDRs: %w", value, err)
	}
	return cidrs, nil
}

func normalizeCIDR(value string) (string, error) {
	_, network, err := net.ParseCIDR(value)
	if err != nil {
		return "", fmt.Errorf("invalid CIDR %q: %w", value, err)
	}
	return network.String(), nil
}

func ipToCIDR(value string) (string, error) {
	ip, err := NormalizeIP(value)
	if err != nil {
		return "", fmt.Errorf("invalid IP address %q: %w", value, err)
	}
	bits := 32
	if ip.To4() == nil {
		bits = 128
	}
	return fmt.Sprintf("%s/%d", ip.String(), bits), nil
}

func simplifyOVNAddress(cidr string) string {
	if before, found := strings.CutSuffix(cidr, "/32"); found {
		return before
	}
	if before, found := strings.CutSuffix(cidr, "/128"); found {
		return before
	}
	return cidr
}

func NormalizeAddressSetEntries(raw string) set.Set[string] {
	clean := strings.ReplaceAll(raw, "\"", "")
	tokens := strings.Fields(strings.TrimSpace(clean))
	return set.New(tokens...)
}

func IPPoolAddressSetName(name string) string {
	return strings.ReplaceAll(name, "-", ".")
}

func NormalizeIP(value string) (net.IP, error) {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return nil, fmt.Errorf("invalid IP address %q", value)
	}
	if v4 := ip.To4(); v4 != nil {
		return v4, nil
	}
	return ip.To16(), nil
}

func IPRangeToCIDRs(start, end net.IP) ([]string, error) {
	length := net.IPv4len
	totalBits := 32
	if start.To4() == nil {
		length = net.IPv6len
		totalBits = 128
	}

	startInt := ipToBigInt(start)
	endInt := ipToBigInt(end)
	if startInt.Cmp(endInt) > 0 {
		return nil, fmt.Errorf("range %s..%s start is greater than end", start, end)
	}

	result := make([]string, 0)
	tmp := new(big.Int)
	for startInt.Cmp(endInt) <= 0 {
		zeros := countTrailingZeros(startInt, totalBits)
		if zeros > totalBits {
			return nil, fmt.Errorf("trailing zero count %d exceeds total bits %d", zeros, totalBits)
		}

		diff := tmp.Sub(endInt, startInt)
		diff.Add(diff, big.NewInt(1))

		var maxDiff int
		if bits := diff.BitLen(); bits > 0 {
			maxDiff = bits - 1
		}

		size := min(zeros, maxDiff)
		size = min(size, totalBits)
		if size < 0 {
			return nil, fmt.Errorf("calculated negative prefix size %d", size)
		}

		prefix := totalBits - size
		networkInt := new(big.Int).Set(startInt)
		networkIP := bigIntToIP(networkInt, length)
		network := &net.IPNet{IP: networkIP, Mask: net.CIDRMask(prefix, totalBits)}
		result = append(result, network.String())

		increment := new(big.Int).Lsh(big.NewInt(1), uint(size))
		startInt.Add(startInt, increment)
	}

	return result, nil
}

func ipToBigInt(ip net.IP) *big.Int {
	return new(big.Int).SetBytes(ip)
}

func bigIntToIP(value *big.Int, length int) net.IP {
	bytes := value.Bytes()
	if len(bytes) < length {
		padded := make([]byte, length)
		copy(padded[length-len(bytes):], bytes)
		bytes = padded
	} else if len(bytes) > length {
		bytes = bytes[len(bytes)-length:]
	}

	ip := make(net.IP, length)
	copy(ip, bytes)
	if length == net.IPv4len {
		return ip.To4()
	}
	return ip
}

func countTrailingZeros(value *big.Int, totalBits int) int {
	if value.Sign() == 0 {
		return totalBits
	}

	zeros := 0
	for zeros < totalBits && value.Bit(zeros) == 0 {
		zeros++
	}
	return zeros
}

func compareIP(a, b net.IP) int {
	return bytes.Compare(a, b)
}
