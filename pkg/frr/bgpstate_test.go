package frr

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const bgpSummarySample = `{
"ipv4Unicast":{
"routerId":"10.0.0.11",
"as":65001,
"vrfId":0,
"vrfName":"default",
"tableVersion":4,
"ribCount":3,
"ribMemory":552,
"peerCount":2,
"peerMemory":58384,
"peers":{
"10.0.0.254":{
"hostname":"tor-a",
"remoteAs":65000,
"localAs":65001,
"version":4,
"msgRcvd":120,
"msgSent":124,
"tableVersion":0,
"outq":0,
"inq":0,
"peerUptime":"01:02:03",
"peerUptimeMsec":3723000,
"peerUptimeEstablishedEpoch":1700000000,
"pfxRcd":0,
"pfxSnt":3,
"state":"Established",
"peerState":"OK",
"connectionsEstablished":1,
"connectionsDropped":0,
"idType":"ipv4"
},
"10.0.0.253":{
"remoteAs":65000,
"localAs":65001,
"version":4,
"msgRcvd":0,
"msgSent":0,
"tableVersion":0,
"outq":0,
"inq":0,
"peerUptime":"never",
"peerUptimeMsec":0,
"pfxRcd":0,
"pfxSnt":0,
"state":"Idle",
"peerState":"OK",
"connectionsEstablished":0,
"connectionsDropped":0,
"idType":"ipv4"
}
},
"failedPeers":1,
"displayedPeers":2,
"totalPeers":2,
"dynamicPeers":0,
"bestPath":{
"multiPathRelax":"false"
}
}
}
`

const bgpRoutesSample = `{
"vrfId": 0,
"vrfName": "default",
"tableVersion": 4,
"routerId": "10.0.0.11",
"defaultLocPrf": 100,
"localAS": 65001,
"routes": { "10.100.0.5/32": [
  {
    "valid":true,
    "bestpath":true,
    "selectionReason":"First path received",
    "pathFrom":"external",
    "prefix":"10.100.0.5",
    "prefixLen":32,
    "network":"10.100.0.5\/32",
    "version":1,
    "metric":0,
    "weight":32768,
    "peerId":"(unspec)",
    "path":"",
    "origin":"incomplete",
    "nexthops":[
      {
        "ip":"10.0.0.21",
        "hostname":"node-1",
        "afi":"ipv4",
        "used":true
      }
    ]
  }
],"10.100.0.6/32": [
  {
    "valid":true,
    "bestpath":true,
    "pathFrom":"external",
    "prefix":"10.100.0.6",
    "prefixLen":32,
    "network":"10.100.0.6\/32",
    "version":2,
    "metric":0,
    "weight":32768,
    "peerId":"(unspec)",
    "path":"",
    "origin":"incomplete",
    "nexthops":[
      {
        "ip":"10.0.0.21",
        "hostname":"node-1",
        "afi":"ipv4",
        "used":true
      }
    ]
  },
  {
    "valid":true,
    "pathFrom":"external",
    "prefix":"10.100.0.6",
    "prefixLen":32,
    "network":"10.100.0.6\/32",
    "version":2,
    "metric":0,
    "weight":32768,
    "peerId":"(unspec)",
    "path":"",
    "origin":"incomplete",
    "nexthops":[
      {
        "ip":"10.0.0.21",
        "hostname":"node-1",
        "afi":"ipv4",
        "used":true
      }
    ]
  }
],"10.200.0.9/32": [
  {
    "valid":true,
    "bestpath":true,
    "pathFrom":"external",
    "prefix":"10.200.0.9",
    "prefixLen":32,
    "network":"10.200.0.9\/32",
    "version":3,
    "metric":0,
    "weight":32768,
    "peerId":"(unspec)",
    "path":"",
    "origin":"incomplete",
    "nexthops":[
      {
        "ip":"10.0.0.22",
        "hostname":"node-1",
        "afi":"ipv4",
        "used":true
      }
    ]
  }
] }  }
`

func TestParseBgpSummary(t *testing.T) {
	peers, err := ParseBgpSummary([]byte(bgpSummarySample))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}
	if got := peers["10.0.0.254"]; !got.Established || got.PrefixesSent != 3 {
		t.Errorf("expected the established peer with 3 prefixes sent, got %+v", got)
	}
	if got := peers["10.0.0.253"]; got.Established || got.PrefixesSent != 0 {
		t.Errorf("expected the idle peer with 0 prefixes sent, got %+v", got)
	}
}

func TestParseBgpSummaryWithoutBgpInstance(t *testing.T) {
	for _, data := range []string{"{}", "{\"ipv4Unicast\":{}}"} {
		peers, err := ParseBgpSummary([]byte(data))
		if err != nil {
			t.Errorf("%s: unexpected error: %v", data, err)
		}
		if len(peers) != 0 {
			t.Errorf("%s: expected no peers, got %+v", data, peers)
		}
	}
	if _, err := ParseBgpSummary([]byte("% BGP instance not found")); err == nil {
		t.Error("expected an error for non-json output")
	}
}

func TestCountPrefixesByNextHop(t *testing.T) {
	counts, err := CountPrefixesByNextHop([]byte(bgpRoutesSample))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if counts["10.0.0.21"] != 2 {
		t.Errorf("expected 2 prefixes through 10.0.0.21, one per prefix even with two paths, got %d", counts["10.0.0.21"])
	}
	if counts["10.0.0.22"] != 1 {
		t.Errorf("expected 1 prefix through 10.0.0.22, got %d", counts["10.0.0.22"])
	}
	if counts["10.0.0.23"] != 0 {
		t.Errorf("expected 0 prefixes through an lrp with no routes, got %d", counts["10.0.0.23"])
	}
}

func TestCountPrefixesByNextHopWithoutRoutes(t *testing.T) {
	counts, err := CountPrefixesByNextHop([]byte("{}"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(counts) != 0 {
		t.Errorf("expected no next hops, got %+v", counts)
	}
}

func TestReadSnapshotIgnoresStaleFiles(t *testing.T) {
	dir := t.TempDir()
	if _, ok := readSnapshot(dir, bgpSummaryFileName); ok {
		t.Error("expected no snapshot when the file is missing")
	}

	path := filepath.Join(dir, bgpSummaryFileName)
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, ok := readSnapshot(dir, bgpSummaryFileName); !ok || string(data) != "{}" {
		t.Errorf("expected the fresh snapshot, got %q %v", data, ok)
	}

	old := time.Now().Add(-2 * snapshotMaxAge)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, ok := readSnapshot(dir, bgpSummaryFileName); ok {
		t.Error("expected a snapshot older than the max age to be ignored")
	}
}
