package util

import (
	"sort"
	"testing"
)

func TestSha256Hash(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		output string
	}{
		{
			name:   "Empty input",
			input:  []byte(""),
			output: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name:   "Non empty input",
			input:  []byte("hello"),
			output: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Sha256Hash(tt.input)
			if got != tt.output {
				t.Errorf("got %v, but want %v", got, tt.output)
			}
		})
	}
}

func TestSha256HashGatewayChassisDistribution(t *testing.T) {
	chassises := []string{
		"efa09809-38e5-4c6d-b0a3-c8729fc40313",
		"672a26c8-c12b-4853-907c-d3243c20e77d",
		"4ac8c950-8839-4b72-93e5-3bab702657db",
	}

	getFirstChassis := func(vpcName string) string {
		sorted := make([]string, len(chassises))
		copy(sorted, chassises)
		sort.Slice(sorted, func(i, j int) bool {
			return Sha256Hash([]byte(vpcName+sorted[i])) < Sha256Hash([]byte(vpcName+sorted[j]))
		})
		return sorted[0]
	}

	first := getFirstChassis("vpc-a")
	for range 10 {
		if getFirstChassis("vpc-a") != first {
			t.Fatal("not deterministic")
		}
	}

	results := map[string]int{}
	vpcs := []string{"vpc-a", "vpc-b", "vpc-c", "vpc-d", "vpc-e", "vpc-f", "vpc-g", "vpc-h", "vpc-i", "vpc-j"}
	for _, vpc := range vpcs {
		results[getFirstChassis(vpc)]++
	}
	if len(results) == 1 {
		t.Fatalf("all VPCs got same chassis: %v", results)
	}
}
