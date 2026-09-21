package util

import (
	"fmt"
	"strings"

	"golang.org/x/sys/unix"

	fabricv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/fabric/v1"
)

func ProtocolToFamily(protocol string) (int, error) {
	switch protocol {
	case fabricv1.ProtocolDual:
		return unix.AF_UNSPEC, nil
	case fabricv1.ProtocolIPv4:
		return unix.AF_INET, nil
	case fabricv1.ProtocolIPv6:
		return unix.AF_INET6, nil
	default:
		return -1, fmt.Errorf("invalid protocol: %s", protocol)
	}
}

func NormalizeIPFamily(ipFamily string) string {
	switch ipFamily {
	case strings.ToLower(fabricv1.ProtocolIPv4):
		return fabricv1.ProtocolIPv4
	case strings.ToLower(fabricv1.ProtocolIPv6):
		return fabricv1.ProtocolIPv6
	default:
		return ipFamily
	}
}
