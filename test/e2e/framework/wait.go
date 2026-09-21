/*
Copyright 2019 The Kubernetes Authors.

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
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
)

func handleWaitingAPIError(err error, retryNotFound bool, taskFormat string, taskArgs ...any) (bool, error) {
	taskDescription := fmt.Sprintf(taskFormat, taskArgs...)
	if retryNotFound && apierrors.IsNotFound(err) {
		Logf("Ignoring NotFound error while " + taskDescription)
		return false, nil
	}
	if retry, delay := shouldRetry(err); retry {
		Logf("Retryable error while %s, retrying after %v: %v", taskDescription, delay, err)
		if delay > 0 {
			time.Sleep(delay)
		}
		return false, nil
	}
	Logf("Encountered non-retryable error while %s: %v", taskDescription, err)
	return false, err
}

func shouldRetry(err error) (retry bool, retryAfter time.Duration) {
	if delay, shouldRetry := apierrors.SuggestsClientDelay(err); shouldRetry {
		return shouldRetry, time.Duration(delay) * time.Second
	}

	if apierrors.IsTimeout(err) || apierrors.IsTooManyRequests(err) || apierrors.IsConflict(err) {
		return true, 0
	}

	return false, 0
}

func WaitUntil(interval, timeout time.Duration, cond func(context.Context) (bool, error), condDesc string) {
	ginkgo.GinkgoHelper()

	if err := wait.PollUntilContextTimeout(context.Background(), interval, timeout, false, cond); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			Failf("timed out while waiting for the condition to be met: %s", condDesc)
		}
		Failf("error occurred while waiting for the condition %q to be met", condDesc)
	}
}
