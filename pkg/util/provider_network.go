package util

import (
	"fmt"
	"slices"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	fabricv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/fabric/v1"
)

func NodeMatchesSelector(node *v1.Node, selector *metav1.LabelSelector) (bool, error) {
	if selector == nil {
		return true, nil
	}

	labelSelector, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return false, err
	}

	return labelSelector.Matches(labels.Set(node.Labels)), nil
}

func IsNodeExcludedFromProviderNetwork(node *v1.Node, pn *fabricv1.ProviderNetwork) (bool, error) {
	if pn.Spec.NodeSelector != nil {
		matched, err := NodeMatchesSelector(node, pn.Spec.NodeSelector)
		if err != nil {
			return false, fmt.Errorf("failed to check nodeSelector for provider network %s: %w", pn.Name, err)
		}
		return !matched, nil
	}

	return slices.Contains(pn.Spec.ExcludeNodes, node.Name), nil
}
