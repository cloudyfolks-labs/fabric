package ovs

import (
	"context"
	"fmt"
	"maps"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"k8s.io/klog/v2"
	"k8s.io/utils/set"

	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

var limiter = new(Limiter)

var readOnlyCommands = set.New(
	"",
	"show",
	"list-br",
	"br-exists",
	"br-to-vlan",
	"br-to-parent",
	"br-get-external-id",
	"list-ports",
	"port-to-br",
	"list-ifaces",
	"iface-to-br",
	"get-controller",
	"get-fail-mode",
	"get-manager",
	"get-ssl",
	"get-aa-mapping",
	"list-zone-limits",
	"list",
	"find",
	"get",
	"wait-until",
)

func UpdateOVSVsctlLimiter(c int32) {
	if c >= 0 {
		limiter.Update(c)
		klog.V(4).Infof("update ovs-vsctl concurrency limit to %d", limiter.Limit())
	}
}

// Glory belongs to openvswitch/ovn-kubernetes
// https://github.com/openvswitch/ovn-kubernetes/blob/master/go-controller/pkg/util/ovs.go

var podNetNsRegexp = regexp.MustCompile(`pod_netns="([^"]+)"`)

func Exec(args ...string) (string, error) {
	var command string
	for arg := range slices.Values(args) {
		if !strings.HasPrefix(arg, "-") {
			command = arg
			break
		}
	}

	if !readOnlyCommands.Has(command) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		if err := limiter.Wait(ctx); err != nil {
			klog.V(4).Infof("command %s %s waiting for execution timeout by concurrency limit of %d", OvsVsCtl, strings.Join(args, " "), limiter.Limit())
			return "", err
		}
		defer limiter.Done()
		klog.V(4).Infof("command %s %s waiting for execution concurrency %d/%d", OvsVsCtl, strings.Join(args, " "), limiter.Current(), limiter.Limit())
	}

	start := time.Now()
	args = slices.Insert(args, 0, "--timeout=30")
	output, err := exec.Command(OvsVsCtl, args...).CombinedOutput()
	elapsed := float64(time.Since(start) / time.Millisecond)
	klog.V(4).Infof("command %s %s in %vms", OvsVsCtl, strings.Join(args, " "), elapsed)

	code := "0"
	defer func() {
		ovsClientRequestLatency.WithLabelValues("ovsdb", command, code).Observe(elapsed)
	}()

	if err != nil {
		code = "1"
		klog.Warningf("ovs-vsctl command error: %s %s in %vms", OvsVsCtl, strings.Join(args, " "), elapsed)
		return "", fmt.Errorf("failed to run '%s %s': %w\n  %q", OvsVsCtl, strings.Join(args, " "), err, output)
	} else if elapsed > 500 {
		klog.Warningf("ovs-vsctl command took too long: %s %s in %vms", OvsVsCtl, strings.Join(args, " "), elapsed)
	}

	return trimCommandOutput(output), nil
}

func ovsCreate(table string, values ...string) (string, error) {
	args := append([]string{"create", table}, values...)
	return Exec(args...)
}

func ovsDestroy(table, record string) error {
	_, err := Exec("--if-exists", "destroy", table, record)
	return err
}

func Set(table, record string, values ...string) error {
	args := append([]string{"set", table, record}, values...)
	_, err := Exec(args...)
	return err
}

func ovsAdd(table, record, column string, values ...string) error {
	args := append([]string{"add", table, record, column}, values...)
	_, err := Exec(args...)
	return err
}

func ovsFind(table, column string, conditions ...string) ([]string, error) {
	args := make([]string, len(conditions)+4)
	args[0], args[1], args[2], args[3] = "--no-heading", "--columns="+column, "find", table
	copy(args[4:], conditions)
	output, err := Exec(args...)
	if err != nil {
		klog.Error(err)
		return nil, err
	}
	ret := parseOvsFindOutput(output)
	return ret, nil
}

func parseOvsFindOutput(output string) []string {
	values := strings.Split(output, "\n\n")

	for i, val := range values {
		if unquoted, err := strconv.Unquote(val); err == nil {
			values[i] = unquoted
		}
	}
	ret := make([]string, 0, len(values))
	for _, val := range values {
		if strings.TrimSpace(val) != "" {
			ret = append(ret, strings.Trim(strings.TrimSpace(val), "\""))
		}
	}
	return ret
}

func Remove(table, record, column string, keys ...string) error {
	args := append([]string{"remove", table, record, column}, keys...)
	_, err := Exec(args...)
	return err
}

func ovsClear(table, record string, columns ...string) error {
	args := append([]string{"--if-exists", "clear", table, record}, columns...)
	_, err := Exec(args...)
	return err
}

func Get(table, record, column, key string, ifExists bool) (string, error) {
	var columnVal string
	if key == "" {
		columnVal = column
	} else {
		columnVal = column + ":" + key
	}
	args := []string{"get", table, record, columnVal}
	if ifExists {
		args = append([]string{"--if-exists"}, args...)
	}
	return Exec(args...)
}

func Bridges() ([]string, error) {
	return ovsFind("bridge", "name", "external-ids:vendor="+util.VendorTag)
}

func BridgeExists(name string) (bool, error) {
	bridges, err := Bridges()
	if err != nil {
		klog.Error(err)
		return false, err
	}
	return slices.Contains(bridges, name), nil
}

func PortExists(name string) (bool, error) {
	result, err := ovsFind("port", "_uuid", "name="+name)
	if err != nil {
		klog.Errorf("failed to find port with name %s: %v", name, err)
		return false, err
	}
	return len(result) != 0, nil
}

func GetQosList(podName, podNamespace, ifaceID string) ([]string, error) {
	var qosList []string
	var err error

	if ifaceID != "" {
		qosList, err = ovsFind("qos", "_uuid", fmt.Sprintf(`external-ids:iface-id="%s"`, ifaceID))
		if err != nil {
			klog.Error(err)
			return qosList, err
		}
	} else {
		qosList, err = ovsFind("qos", "_uuid", fmt.Sprintf(`external-ids:pod="%s/%s"`, podNamespace, podName))
		if err != nil {
			klog.Error(err)
			return qosList, err
		}
	}

	return qosList, nil
}

func ClearPodBandwidth(podName, podNamespace, ifaceID string) error {
	qosList, err := GetQosList(podName, podNamespace, ifaceID)
	if err != nil {
		klog.Error(err)
		return err
	}

	// https://github.com/cloudyfolks-labs/fabric/issues/1191
	usedQosList, err := ovsFind("port", "qos", "qos!=[]")
	if err != nil {
		klog.Error(err)
		return err
	}

	for _, qosID := range qosList {
		found := slices.Contains(usedQosList, qosID)
		if found {
			continue
		}

		if err := ovsDestroy("qos", qosID); err != nil {
			klog.Error(err)
			return err
		}
	}
	return nil
}

var lastInterfacePodMap map[string]string

func ListInterfacePodMap() (map[string]string, error) {
	output, err := Exec("--data=bare", "--format=csv", "--no-heading", "--columns=name,error,external_ids", "find",
		"interface", "external_ids:pod_name!=[]", "external_ids:pod_namespace!=[]", "link_state!=up")
	if err != nil {
		klog.Errorf("failed to list interface, %v", err)
		return nil, err
	}
	lines := strings.Split(output, "\n")
	result := make(map[string]string, len(lines))
	for _, l := range lines {
		if len(strings.TrimSpace(l)) == 0 {
			continue
		}
		parts := strings.SplitN(strings.TrimSpace(l), ",", 3)
		if len(parts) != 3 {
			continue
		}
		ifaceName := strings.TrimSpace(parts[0])
		errText := strings.TrimSpace(parts[1])
		var podNamespace, podName string
		for externalID := range strings.FieldsSeq(parts[2]) {
			if strings.Contains(externalID, "pod_name=") {
				podName = strings.TrimPrefix(strings.TrimSpace(externalID), "pod_name=")
			}

			if strings.Contains(externalID, "pod_namespace=") {
				podNamespace = strings.TrimPrefix(strings.TrimSpace(externalID), "pod_namespace=")
			}
		}
		result[ifaceName] = fmt.Sprintf("%s/%s/%s", podNamespace, podName, errText)
	}
	if !maps.Equal(result, lastInterfacePodMap) {
		klog.Infof("interface pod map: %v", result)
		lastInterfacePodMap = maps.Clone(result)
	}
	return result, nil
}

func CleanInterface(name string) error {
	qosList, err := ovsFind("port", "qos", "name="+name)
	if err != nil {
		klog.Errorf("failed to find related port %v", err)
		return err
	}
	klog.Infof("delete lost port %s", name)
	output, err := Exec("--if-exists", "--with-iface", "del-port", name)
	if err != nil {
		klog.Errorf("failed to delete ovs port %v, %s", err, output)
		return err
	}
	for _, qos := range qosList {
		qos = strings.TrimSpace(qos)
		if qos != "" && qos != "[]" {
			klog.Infof("delete lost qos %s", qos)
			err = ovsDestroy("qos", qos)
			if err != nil {
				klog.Errorf("failed to delete qos %s, %v", qos, err)
				return err
			}
		}
	}
	return nil
}

// Find and remove any existing OVS port with this iface-id. Pods can
// have multiple sandboxes if some are waiting for garbage collection,
// but only the latest one should have the iface-id set.
// See: https://github.com/ovn-org/ovn-kubernetes/pull/869
func CleanDuplicatePort(ifaceID, portName string) {
	uuids, _ := ovsFind("Interface", "_uuid", "external-ids:iface-id="+ifaceID, "name!="+portName)
	for _, uuid := range uuids {
		if out, err := Exec("remove", "Interface", uuid, "external-ids", "iface-id"); err != nil {
			klog.Errorf("failed to clear stale OVS port %q iface-id %q: %v\n  %q", uuid, ifaceID, err, out)
		}
	}
}

func ValidatePortVendor(port string) (bool, error) {
	output, err := ovsFind("Port", "name", "external_ids:vendor="+util.VendorTag)
	return slices.Contains(output, port), err
}

func GetInterfacePodNs(iface string) (string, error) {
	ret, err := ovsFind("interface", "external-ids", "external-ids:iface-id="+iface)
	if err != nil {
		klog.Error(err)
		return "", err
	}

	if len(ret) == 0 {
		return "", nil
	}

	podNetNs := ""
	match := podNetNsRegexp.FindStringSubmatch(ret[0])
	if len(match) > 1 {
		podNetNs = match[1]
	}

	return podNetNs, nil
}

func ConfigInterfaceMirror(globalMirror bool, open, iface string) error {
	if globalMirror {
		return nil
	}

	interfaceList, err := ovsFind("interface", "name", "external-ids:iface-id="+iface)
	if err != nil {
		klog.Error(err)
		return err
	}
	for _, ifName := range interfaceList {
		portUUIDs, err := ovsFind("port", "_uuid", "name="+ifName)
		if err != nil {
			klog.Error(err)
			return err
		}
		if len(portUUIDs) != 1 {
			return fmt.Errorf("find port failed, portName=%s", ifName)
		}
		portID := portUUIDs[0]
		if open == "true" {
			err = ovsAdd("mirror", util.MirrorDefaultName, "select_dst_port", portID)
			if err != nil {
				klog.Error(err)
				return err
			}
		} else {
			mirrorPorts, err := ovsFind("mirror", "select_dst_port", "name="+util.MirrorDefaultName)
			if err != nil {
				klog.Error(err)
				return err
			}
			if len(mirrorPorts) == 0 {
				return fmt.Errorf("find mirror failed, mirror name=%s", util.MirrorDefaultName)
			}
			if len(mirrorPorts) > 1 {
				return fmt.Errorf("repeated mirror data, mirror name=%s", util.MirrorDefaultName)
			}
			for _, mirrorPortIDs := range mirrorPorts {
				if strings.Contains(mirrorPortIDs, portID) {
					_, err := Exec("remove", "mirror", util.MirrorDefaultName, "select_dst_port", portID)
					if err != nil {
						klog.Error(err)
						return err
					}
				}
			}
		}
	}
	return nil
}

func ClearPortQosBinding(ifaceID string) error {
	interfaceList, err := ovsFind("interface", "name", fmt.Sprintf(`external-ids:iface-id="%s"`, ifaceID))
	if err != nil {
		klog.Error(err)
		return err
	}

	for _, ifName := range interfaceList {
		if err = ovsClear("port", ifName, "qos"); err != nil {
			klog.Error(err)
			return err
		}
	}
	return nil
}

func ListExternalIDs(table string) (map[string]string, error) {
	output, err := Exec("--data=bare", "--format=csv", "--no-heading", "--columns=_uuid,external_ids", "find", table, "external_ids:iface-id!=[]")
	if err != nil {
		klog.Errorf("failed to list %s, %v", table, err)
		return nil, err
	}
	lines := strings.Split(output, "\n")
	result := make(map[string]string, len(lines))
	for _, l := range lines {
		if len(strings.TrimSpace(l)) == 0 {
			continue
		}
		parts := strings.Split(strings.TrimSpace(l), ",")
		if len(parts) != 2 {
			continue
		}
		uuid := strings.TrimSpace(parts[0])
		for externalID := range strings.FieldsSeq(parts[1]) {
			if !strings.Contains(externalID, "iface-id=") {
				continue
			}
			iface := strings.TrimPrefix(strings.TrimSpace(externalID), "iface-id=")
			result[iface] = uuid
			break
		}
	}
	return result, nil
}

func ListQosQueueIDs() (map[string]string, error) {
	output, err := Exec("--data=bare", "--format=csv", "--no-heading", "--columns=_uuid,queues", "find", "qos", "queues:0!=[]")
	if err != nil {
		klog.Errorf("failed to list qos, %v", err)
		return nil, err
	}
	lines := strings.Split(output, "\n")
	result := make(map[string]string, len(lines))
	for _, l := range lines {
		if len(strings.TrimSpace(l)) == 0 {
			continue
		}
		parts := strings.Split(strings.TrimSpace(l), ",")
		if len(parts) != 2 {
			continue
		}
		qosID := strings.TrimSpace(parts[0])
		if !strings.Contains(strings.TrimSpace(parts[1]), "0=") {
			continue
		}
		queueID := strings.TrimPrefix(strings.TrimSpace(parts[1]), "0=")
		result[qosID] = queueID
	}
	return result, nil
}
