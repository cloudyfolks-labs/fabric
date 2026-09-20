package daemon

import (
	"fmt"
	"os"
	"runtime"
	"sync"

	"github.com/containernetworking/plugins/pkg/ns"
	"golang.org/x/sys/unix"
)

// this file is copied from https://github.com/containerd/containerd/blob/main/pkg/netns/netns_linux.go

func getCurrentThreadNetNSPath() string {
	return fmt.Sprintf("/proc/%d/task/%d/ns/net", os.Getpid(), unix.Gettid())
}

func newNetNS(path string) error {
	mountPointFd, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return err
	}
	mountPointFd.Close()

	defer func() {
		if err != nil {
			os.RemoveAll(path)
		}
	}()

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		runtime.LockOSThread()

		var origNS ns.NetNS
		if origNS, err = ns.GetNS(getCurrentThreadNetNSPath()); err != nil {
			return
		}
		defer origNS.Close()

		err = unix.Unshare(unix.CLONE_NEWNET)
		if err != nil {
			return
		}

		defer func() { _ = origNS.Set() }()

		err = unix.Mount(getCurrentThreadNetNSPath(), path, "none", unix.MS_BIND, "")
		if err != nil {
			err = fmt.Errorf("failed to bind mount ns at %s: %w", path, err)
		}
	}()
	wg.Wait()

	if err != nil {
		return fmt.Errorf("failed to create namespace: %w", err)
	}

	return nil
}
