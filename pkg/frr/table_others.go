//go:build !linux

package frr

import "errors"

func tableRouteCount(uint32) (int, error) {
	return 0, errors.New("kernel route tables are only readable on linux")
}
