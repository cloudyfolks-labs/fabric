package ovs

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/ovn-kubernetes/libovsdb/ovsdb"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

func OvsdbServerAddress(host string, port intstr.IntOrString) string {
	scheme := "tcp"
	if os.Getenv(util.EnvSSLEnabled) == "true" {
		scheme = "ssl"
	}
	return fmt.Sprintf("%s:%s", scheme, net.JoinHostPort(host, port.String()))
}

func Query(address, database string, timeout int, operations ...ovsdb.Operation) ([]ovsdb.OperationResult, error) {
	transArgs := ovsdb.NewTransactArgs(database, operations...)
	query, err := json.Marshal(transArgs)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ovsdb transaction args %+v: %w", transArgs, err)
	}

	args := []string{"--timeout", strconv.Itoa(timeout), "query", address, string(query)}
	if strings.HasPrefix(address, "ssl:") {
		args = slices.Insert(args, 0, CmdSSLArgs()...)
	}

	output, err := exec.Command("ovsdb-client", args...).CombinedOutput() // #nosec G204
	if err != nil {
		return nil, fmt.Errorf("failed to execute ovsdb-client with args %v: %w\noutput: %s", args, err, string(output))
	}

	var results []ovsdb.OperationResult
	if err = json.Unmarshal(output, &results); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ovsdb-client output %q: %w", string(output), err)
	}

	return results, nil
}
