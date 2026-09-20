/*
Copyright 2014 The Kubernetes Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package framework

import (
	"fmt"
	"regexp"
	"runtime"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/format"

	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

var (
	macRegex  = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}([0-9A-Fa-f]{2})$`)
	uuidRegex = regexp.MustCompile(`^[0-9a-f]{8}-([0-9a-f]{4}-){3}[0-9a-f]{12}$`)
)

func buildDescription(explain ...any) string {
	switch len(explain) {
	case 0:
		return ""
	case 1:
		if describe, ok := explain[0].(func() string); ok {
			return describe() + "\n"
		}
	}
	return fmt.Sprintf(explain[0].(string), explain[1:]...) + "\n"
}

func buildExplainWithOffset(offset int, explain ...any) string {
	cl := types.NewCodeLocation(3)
	_, file, line, _ := runtime.Caller(offset + 2)
	description := buildDescription(explain...)
	if cl.FileName == file && cl.LineNumber == line {
		return description
	}

	return description + fmt.Sprintf("Code Location: %s:%d", file, line)
}

func buildExplain(explain ...any) string {
	return buildExplainWithOffset(1, explain...)
}

func ExpectEqual(actual, extra any, explain ...any) {
	gomega.ExpectWithOffset(1, actual).To(gomega.Equal(extra), buildExplain(explain...))
}

func ExpectNotEqual(actual, extra any, explain ...any) {
	gomega.ExpectWithOffset(1, actual).NotTo(gomega.Equal(extra), buildExplain(explain...))
}

func ExpectError(err error, explain ...any) {
	gomega.ExpectWithOffset(1, err).To(gomega.HaveOccurred(), buildExplain(explain...))
}

func ExpectNoError(err error, explain ...any) {
	ExpectNoErrorWithOffset(1, err, buildExplain(explain...))
}

func ExpectNoErrorWithOffset(offset int, err error, explain ...any) {
	if err == nil {
		return
	}

	prefix := ""
	if len(explain) > 0 {
		if str, ok := explain[0].(string); ok {
			prefix = fmt.Sprintf(str, explain[1:]...) + ": "
		} else {
			prefix = fmt.Sprintf("unexpected explain arguments, need format string: %v", explain)
		}
	}

	// This intentionally doesn't use gomega.Expect. Instead，we take
	// full control over what information is presented where:
	// - The complete error object is logged because it may contain
	//   additional information that isn't included in its error
	//   string.
	// - It is not included in the failure message because
	//   it might make the failure message very large and/or
	//   cause error aggregation to work less well: two
	//   failures at the same code line might not be matched in
	//   https://go.k8s.io/triage because the error details are too
	//   different.
	Logf("Unexpected error: %s\n%s", prefix, format.Object(err, 1))
	Fail(prefix+err.Error(), 1+offset)
}

func ExpectConsistOf(actual, extra any, explain ...any) {
	gomega.ExpectWithOffset(1, actual).To(gomega.ConsistOf(extra), buildExplain(explain...))
}

func ExpectContainElement(actual, extra any, explain ...any) {
	gomega.ExpectWithOffset(1, actual).To(gomega.ContainElement(extra), buildExplain(explain...))
}

func ExpectNotContainElement(actual, extra any, explain ...any) {
	gomega.ExpectWithOffset(1, actual).NotTo(gomega.ContainElement(extra), buildExplain(explain...))
}

func ExpectContainSubstring(actual, substr string, explain ...any) {
	gomega.ExpectWithOffset(1, actual).To(gomega.ContainSubstring(substr), buildExplain(explain...))
}

func ExpectNotContainSubstring(actual, substr string, explain ...any) {
	gomega.ExpectWithOffset(1, actual).NotTo(gomega.ContainSubstring(substr), buildExplain(explain...))
}

func ExpectHaveKey(actual, key any, explain ...any) {
	gomega.ExpectWithOffset(1, actual).To(gomega.HaveKey(key), buildExplain(explain...))
}

func ExpectHaveKeyWithValue(actual, key, value any, explain ...any) {
	gomega.ExpectWithOffset(1, actual).To(gomega.HaveKeyWithValue(key, value), buildExplain(explain...))
}

func ExpectNotHaveKey(actual, key any, explain ...any) {
	gomega.ExpectWithOffset(1, actual).NotTo(gomega.HaveKey(key), buildExplain(explain...))
}

func ExpectNil(actual any, explain ...any) {
	gomega.ExpectWithOffset(1, actual).To(gomega.BeNil(), buildExplain(explain...))
}

func ExpectNotNil(actual any, explain ...any) {
	gomega.ExpectWithOffset(1, actual).NotTo(gomega.BeNil(), buildExplain(explain...))
}

func ExpectEmpty(actual any, explain ...any) {
	gomega.ExpectWithOffset(1, actual).To(gomega.BeEmpty(), buildExplain(explain...))
}

func ExpectNotEmpty(actual any, explain ...any) {
	gomega.ExpectWithOffset(1, actual).NotTo(gomega.BeEmpty(), buildExplain(explain...))
}

func ExpectHaveLen(actual any, count int, explain ...any) {
	gomega.ExpectWithOffset(1, actual).To(gomega.HaveLen(count), buildExplain(explain...))
}

func ExpectTrue(actual any, explain ...any) {
	gomega.ExpectWithOffset(1, actual).To(gomega.BeTrue(), buildExplain(explain...))
}

func expectTrueWithOffset(offset int, actual any, explain ...any) {
	gomega.ExpectWithOffset(1, actual).To(gomega.BeTrue(), buildExplainWithOffset(offset, explain...))
}

func ExpectFalse(actual any, explain ...any) {
	gomega.ExpectWithOffset(1, actual).NotTo(gomega.BeTrue(), buildExplain(explain...))
}

func ExpectZero(actual any, explain ...any) {
	gomega.ExpectWithOffset(1, actual).To(gomega.BeZero(), buildExplain(explain...))
}

func ExpectNotZero(actual any, explain ...any) {
	gomega.ExpectWithOffset(1, actual).NotTo(gomega.BeZero(), buildExplain(explain...))
}

func ExpectUUID(s string) {
	ginkgo.GinkgoHelper()
	ginkgo.By(fmt.Sprintf("verifying the string %q is an UUID", s))
	expectTrueWithOffset(1, uuidRegex.MatchString(s))
}

func ExpectMAC(s string) {
	ginkgo.GinkgoHelper()
	ginkgo.By(fmt.Sprintf("verifying the string %q is a MAC address", s))
	expectTrueWithOffset(1, macRegex.MatchString(s))
}

func ExpectIPInCIDR(ip, cidr string) {
	ginkgo.GinkgoHelper()
	ginkgo.By(fmt.Sprintf("verifying IP address %q is within the CIDR %q", ip, cidr))
	expectTrueWithOffset(1, util.CIDRContainIP(cidr, ip))
}
