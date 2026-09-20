package util

import (
	"slices"

	"k8s.io/utils/set"
)

func DiffStringSlice(slice1, slice2 []string) []string {
	var diff []string

	for i := range 2 {
		for _, s1 := range slice1 {
			found := slices.Contains(slice2, s1)

			if !found {
				diff = append(diff, s1)
			}
		}

		if i == 0 {
			slice1, slice2 = slice2, slice1
		}
	}
	return diff
}

func UnionStringSlice(slices ...[]string) []string {
	union := set.New[string]()
	for _, s := range slices {
		union.Insert(s...)
	}
	return union.UnsortedList()
}

func IsStringsOverlap(a, b []string) bool {
	for _, sa := range a {
		if slices.Contains(b, sa) {
			return true
		}
	}
	return false
}

func RemoveString(slice []string, s string) []string {
	result := make([]string, 0, len(slice))
	for _, item := range slice {
		if item == s {
			continue
		}
		result = append(result, item)
	}
	return result
}
