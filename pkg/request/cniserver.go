package request

import (
	"fmt"
	"net/http"

	"github.com/containernetworking/cni/pkg/types"
	"github.com/parnurzeal/gorequest"
)

type CniServerClient struct {
	*gorequest.SuperAgent
}

type Route struct {
	Destination string `json:"dst,omitempty"`
	Gateway     string `json:"gw,omitempty"`
}

type IPConfig struct {
	Protocol string `json:"protocol"`
	IP       string `json:"ip"`
	CIDR     string `json:"cidr"`
	Gateway  string `json:"gateway,omitempty"`
}

type CniRequest struct {
	CniType      string    `json:"cni_type"`
	PodName      string    `json:"pod_name"`
	PodNamespace string    `json:"pod_namespace"`
	ContainerID  string    `json:"container_id"`
	NetNs        string    `json:"net_ns"`
	IfName       string    `json:"if_name"`
	Provider     string    `json:"provider"`
	Routes       []Route   `json:"routes"`
	DNS          types.DNS `json:"dns"`
	VfDriver     string    `json:"vf_driver"`

	DeviceID string `json:"deviceID"`

	VhostUserSocketVolumeName  string `json:"vhost_user_socket_volume_name"`
	VhostUserSocketName        string `json:"vhost_user_socket_name"`
	VhostUserSocketConsumption string `json:"vhost_user_socket_consumption"`
}

type CniResponse struct {
	IPs        []IPConfig `json:"ips"`
	MacAddress string     `json:"mac_address"`
	Routes     []Route    `json:"routes"`
	Mtu        int        `json:"mtu"`
	PodNicName string     `json:"nicname"`
	DNS        types.DNS  `json:"dns"`
	Err        string     `json:"error"`
}

func (csc CniServerClient) Add(podRequest CniRequest) (*CniResponse, error) {
	resp := CniResponse{}
	res, _, errors := csc.Post("http://dummy/api/v1/add").Send(podRequest).EndStruct(&resp)
	if len(errors) != 0 {
		return nil, errors[0]
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request ip return %d %s", res.StatusCode, resp.Err)
	}
	return &resp, nil
}

func (csc CniServerClient) Del(podRequest CniRequest) error {
	res, body, errors := csc.Post("http://dummy/api/v1/del").Send(podRequest).End()
	if len(errors) != 0 {
		return errors[0]
	}
	if res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("delete ip return %d %s", res.StatusCode, body)
	}
	return nil
}
