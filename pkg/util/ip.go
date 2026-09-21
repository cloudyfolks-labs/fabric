package util

import (
	"fmt"
	"net"
	"strings"
)

func Uint32ToIPv4(n uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d", n>>24, n&0xff0000>>16, n&0xff00>>8, n&0xff)
}

func IPv4ToUint32(ip net.IP) uint32 {
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

func IPv6ToLabelValue(ip string) string {
	if ip == "" {
		return ""
	}
	v := strings.ReplaceAll(ip, ":", "-")
	if v[0] == '-' {
		v = "0" + v
	}
	if v[len(v)-1] == '-' {
		v += "0"
	}
	return v
}
