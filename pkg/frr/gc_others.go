//go:build !linux

package frr

func purgeStaleOvnRoutes(map[uint32]struct{}) (int, error) {
	return 0, nil
}
