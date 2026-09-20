package util

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"

	nadutils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	v1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/utils/set"
)

func APIResourceExists(discoveryClient discovery.DiscoveryInterface, gv string, kinds ...string) (bool, error) {
	apiResourceLists, err := discoveryClient.ServerResourcesForGroupVersion(gv)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to discover api resources for %s: %w", gv, err)
	}

	existingKinds := set.New[string]()
	for _, apiResource := range apiResourceLists.APIResources {
		existingKinds.Insert(apiResource.Kind)
	}

	return existingKinds.HasAll(kinds...), nil
}

func ObjectMatchesLabelSelector(obj metav1.Object, selector *metav1.LabelSelector) bool {
	labelSelector, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		klog.Errorf("failed to convert label selector %v: %v", selector, err)
		return false
	}
	return labelSelector.Matches(labels.Set(obj.GetLabels()))
}

func DialTCP(host string, timeout time.Duration, verbose bool) error {
	u, err := url.Parse(host)
	if err != nil {
		klog.Error(err)
		return fmt.Errorf("failed to parse host %q: %w", host, err)
	}

	var conn net.Conn
	address := net.JoinHostPort(u.Hostname(), u.Port())
	switch u.Scheme {
	case "tcp", "http":
		conn, err = net.DialTimeout("tcp", address, timeout)
	case "tls", "https":
		config := &tls.Config{InsecureSkipVerify: true} // #nosec G402
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: timeout}, "tcp", address, config)
	default:
		return fmt.Errorf("unsupported scheme %q", u.Scheme)
	}

	if err == nil {
		if verbose {
			klog.Infof("succeeded to dial host %q", host)
		}
		_ = conn.Close()
		return nil
	}

	return fmt.Errorf("timed out dialing host %q", host)
}

func DialAPIServer(host string, interval time.Duration, retry int) error {
	timer := time.NewTimer(interval)
	for range retry {
		err := DialTCP(host, interval, true)
		if err == nil {
			return nil
		}
		klog.Warningf("failed to dial apiserver %q: %v", host, err)
		<-timer.C
		timer.Reset(interval)
	}

	return fmt.Errorf("timed out dialing apiserver %q", host)
}

func GetNodeInternalIP(node v1.Node) (ipv4, ipv6 string) {
	var ips []string
	for _, addr := range node.Status.Addresses {
		if addr.Type == v1.NodeInternalIP {
			ips = append(ips, addr.Address)
		}
	}

	return SplitStringIP(strings.Join(ips, ","))
}

func PodIPs(pod v1.Pod) []string {
	if len(pod.Status.PodIPs) == 0 && pod.Status.PodIP != "" {
		return []string{pod.Status.PodIP}
	}

	ips := make([]string, len(pod.Status.PodIPs))
	for i := range pod.Status.PodIPs {
		ips[i] = pod.Status.PodIPs[i].IP
	}
	return ips
}

func PodAttachmentIPs(pod *v1.Pod, networkName string) ([]string, error) {
	if pod == nil {
		return nil, errors.New("programmatic error: pod is nil")
	}

	statuses, err := nadutils.GetNetworkStatus(pod)
	if err != nil {
		klog.Error(err)
		return nil, fmt.Errorf("failed to get network status for pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}

	for _, status := range statuses {
		if status.Name == networkName {
			return status.IPs, nil
		}
	}

	return nil, fmt.Errorf("pod %s/%s has no network status for network %s", pod.Namespace, pod.Name, networkName)
}

func ServiceClusterIPs(svc v1.Service) []string {
	if len(svc.Spec.ClusterIPs) == 0 && svc.Spec.ClusterIP != v1.ClusterIPNone && svc.Spec.ClusterIP != "" {
		return []string{svc.Spec.ClusterIP}
	}

	ips := make([]string, 0, len(svc.Spec.ClusterIPs))
	for _, ip := range svc.Spec.ClusterIPs {
		if net.ParseIP(ip) == nil {
			if ip != "" && ip != v1.ClusterIPNone {
				klog.Warningf("invalid cluster IP %q for service %s/%s", ip, svc.Namespace, svc.Name)
			}
			continue
		}
		ips = append(ips, ip)
	}
	return ips
}

func LabelSelectorNotEquals(key, value string) (labels.Selector, error) {
	requirement, err := labels.NewRequirement(key, selection.NotEquals, []string{value})
	if err != nil {
		klog.Errorf("failed to create label requirement: %v", err)
		return nil, err
	}
	return labels.Everything().Add(*requirement), nil
}

func LabelSelectorNotEmpty(key string) (labels.Selector, error) {
	return LabelSelectorNotEquals(key, "")
}

func GetTruncatedUID(uid string) string {
	return uid[len(uid)-12:]
}

func SetNodeNetworkUnavailableCondition(cs kubernetes.Interface, nodeName string, status v1.ConditionStatus, reason, message string) error {
	now := metav1.NewTime(time.Now())
	patch := map[string]map[string][]v1.NodeCondition{
		"status": {
			"conditions": []v1.NodeCondition{{
				Type:               v1.NodeNetworkUnavailable,
				Status:             status,
				Reason:             reason,
				Message:            message,
				LastTransitionTime: now,
				LastHeartbeatTime:  now,
			}},
		},
	}
	data, err := json.Marshal(patch)
	if err != nil {
		klog.Errorf("failed to marshal patch data: %v", err)
		return err
	}

	if _, err = cs.CoreV1().Nodes().PatchStatus(context.Background(), nodeName, data); err != nil {
		klog.Errorf("failed to patch node %s: %v", nodeName, err)
		return err
	}

	return nil
}

func InjectedServiceVariables(service string) (string, string) {
	prefix := strings.ToUpper(strings.ReplaceAll(service, "-", "_"))
	return os.Getenv(prefix + "_SERVICE_HOST"), os.Getenv(prefix + "_SERVICE_PORT")
}

func ObjectKind[T metav1.Object]() string {
	typ := reflect.TypeFor[T]()
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	return typ.Name()
}

func TrimManagedFields(obj any) (any, error) {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return nil, err
	}
	if accessor.GetManagedFields() != nil {
		accessor.SetManagedFields(nil)
	}
	return obj, nil
}

func TrimPodForController(obj any) (any, error) {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		return TrimManagedFields(obj)
	}

	pod.ManagedFields = nil

	pod.Spec.Volumes = nil
	pod.Spec.EphemeralContainers = nil
	pod.Spec.Tolerations = nil
	pod.Spec.Affinity = nil
	pod.Spec.NodeSelector = nil
	pod.Spec.TopologySpreadConstraints = nil
	pod.Spec.ReadinessGates = nil
	pod.Spec.HostAliases = nil
	pod.Spec.ImagePullSecrets = nil
	pod.Spec.ResourceClaims = nil
	pod.Spec.SchedulingGates = nil
	pod.Spec.Overhead = nil
	pod.Spec.SecurityContext = nil
	pod.Spec.DNSConfig = nil
	pod.Spec.PriorityClassName = ""
	pod.Spec.Priority = nil
	pod.Spec.PreemptionPolicy = nil
	pod.Spec.RuntimeClassName = nil
	pod.Spec.SchedulerName = ""
	pod.Spec.ServiceAccountName = ""
	pod.Spec.DeprecatedServiceAccount = ""
	pod.Spec.AutomountServiceAccountToken = nil
	pod.Spec.Subdomain = ""
	pod.Spec.Hostname = ""
	pod.Spec.SetHostnameAsFQDN = nil

	trimPodContainers(pod.Spec.Containers)
	trimPodContainers(pod.Spec.InitContainers)

	pod.Status.InitContainerStatuses = nil
	pod.Status.EphemeralContainerStatuses = nil
	pod.Status.ResourceClaimStatuses = nil
	pod.Status.QOSClass = ""
	pod.Status.StartTime = nil
	pod.Status.Message = ""
	pod.Status.NominatedNodeName = ""
	pod.Status.Resize = ""

	trimPodContainerStatuses(pod.Status.ContainerStatuses)
	trimPodConditions(pod.Status.Conditions)

	return pod, nil
}

func trimPodContainers(containers []v1.Container) {
	for i := range containers {
		c := &containers[i]
		c.Image = ""
		c.Command = nil
		c.Args = nil
		c.WorkingDir = ""
		c.Env = nil
		c.EnvFrom = nil
		c.Resources = v1.ResourceRequirements{}
		c.ResizePolicy = nil

		c.VolumeMounts = nil
		c.VolumeDevices = nil
		c.LivenessProbe = nil
		c.ReadinessProbe = nil
		c.StartupProbe = nil
		c.Lifecycle = nil
		c.TerminationMessagePath = ""
		c.TerminationMessagePolicy = ""
		c.ImagePullPolicy = ""
		c.SecurityContext = nil
		c.Stdin = false
		c.StdinOnce = false
		c.TTY = false
	}
}

func trimPodContainerStatuses(statuses []v1.ContainerStatus) {
	for i := range statuses {
		s := &statuses[i]

		s.LastTerminationState = v1.ContainerState{}
		s.Ready = false
		s.Image = ""
		s.ImageID = ""
		s.ContainerID = ""
		s.Started = nil
		s.AllocatedResources = nil
		s.Resources = nil
		s.VolumeMounts = nil
		s.User = nil
		s.AllocatedResourcesStatus = nil
	}
}

func trimPodConditions(conds []v1.PodCondition) {
	for i := range conds {
		c := &conds[i]
		c.LastProbeTime = metav1.Time{}
		c.Reason = ""
		c.Message = ""
		c.ObservedGeneration = 0
	}
}
