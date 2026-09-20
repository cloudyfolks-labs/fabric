// This code below is referenced at https://github.com/Asphaltt/go-tproxy/blob/master/tproxy_tcp.go
// Because the code needs to be customized somewhere, the project is not directly imported
package tproxy

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"

	"k8s.io/klog/v2"
)

type Listener struct {
	base net.Listener
}

func (listener *Listener) Accept() (net.Conn, error) {
	return listener.AcceptTProxy()
}

func (listener *Listener) AcceptTProxy() (*Conn, error) {
	tcpConn, err := listener.base.(*net.TCPListener).AcceptTCP()
	if err != nil {
		klog.Error(err)
		return nil, err
	}

	return &Conn{TCPConn: tcpConn}, nil
}

func (listener *Listener) Addr() net.Addr {
	return listener.base.Addr()
}

func (listener *Listener) Close() error {
	return listener.base.Close()
}

func ListenTCP(network string, laddr *net.TCPAddr) (net.Listener, error) {
	return listenTCP("", network, laddr)
}

func listenTCP(device, network string, laddr *net.TCPAddr) (net.Listener, error) {
	listener, err := net.ListenTCP(network, laddr)
	if err != nil {
		klog.Error(err)
		return nil, err
	}

	fileDescriptorSource, err := listener.File()
	if err != nil {
		return nil, &net.OpError{Op: "listen", Net: network, Source: nil, Addr: laddr, Err: fmt.Errorf("get file descriptor: %w", err)}
	}

	defer func() {
		if err := fileDescriptorSource.Close(); err != nil {
			klog.Errorf("fileDescriptorSource %v Close err: %v", fileDescriptorSource, err)
		}
	}()

	fd := int(fileDescriptorSource.Fd()) // #nosec G115
	if device != "" {
		if err = syscall.BindToDevice(fd, device); err != nil {
			return nil, &net.OpError{Op: "listen", Net: network, Source: nil, Addr: laddr, Err: fmt.Errorf("set socket option: SO_BINDTODEVICE(%s): %w", device, err)}
		}
	}

	if err = syscall.SetsockoptInt(fd, syscall.SOL_IP, syscall.IP_TRANSPARENT, 1); err != nil {
		return nil, &net.OpError{Op: "listen", Net: network, Source: nil, Addr: laddr, Err: fmt.Errorf("set socket option: IP_TRANSPARENT: %w", err)}
	}

	return &Listener{listener}, nil
}

type Conn struct {
	*net.TCPConn
}

func tcpAddrToSocketAddr(addr *net.TCPAddr) (syscall.Sockaddr, error) {
	switch {
	case addr.IP.To4() != nil:
		ip := [4]byte{}
		copy(ip[:], addr.IP.To4())

		return &syscall.SockaddrInet4{Addr: ip, Port: addr.Port}, nil

	default:
		ip := [16]byte{}
		copy(ip[:], addr.IP.To16())

		return &syscall.SockaddrInet6{Addr: ip, Port: addr.Port}, nil
	}
}

func tcpAddrFamily(net string, laddr, raddr *net.TCPAddr) int {
	switch net[len(net)-1] {
	case '4':
		return syscall.AF_INET
	case '6':
		return syscall.AF_INET6
	}

	if (laddr == nil || laddr.IP.To4() != nil) &&
		(raddr == nil || laddr.IP.To4() != nil) {
		return syscall.AF_INET
	}
	return syscall.AF_INET6
}

func DialTCP(laddr, raddr *net.TCPAddr, isnonblocking bool) (*net.TCPConn, error) {
	return dialTCP("", laddr, raddr, false, isnonblocking)
}

func dialTCP(device string, laddr, raddr *net.TCPAddr, dontAssumeRemote, isnonblocking bool) (*net.TCPConn, error) {
	if laddr == nil || raddr == nil {
		return nil, &net.OpError{Op: "dial", Err: errors.New("empty local address or remote address")}
	}

	remoteSocketAddress, err := tcpAddrToSocketAddr(raddr)
	if err != nil {
		klog.Error(err)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("build destination socket address: %w", err)}
	}

	localSocketAddress, err := tcpAddrToSocketAddr(laddr)
	if err != nil {
		klog.Error(err)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("build local socket address: %w", err)}
	}

	fileDescriptor, err := syscall.Socket(tcpAddrFamily("tcp", raddr, laddr), syscall.SOCK_STREAM, syscall.IPPROTO_TCP)
	if err != nil {
		klog.Error(err)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("socket open: %w", err)}
	}

	if device != "" {
		if err = syscall.BindToDevice(fileDescriptor, device); err != nil {
			klog.Error(err)
			return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("set socket option: SO_BINDTODEVICE(%s): %w", device, err)}
		}
	}

	if err = syscall.SetsockoptInt(fileDescriptor, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		if err := syscall.Close(fileDescriptor); err != nil {
			klog.Errorf("fileDescriptor %v Close err: %v", fileDescriptor, err)
		}
		klog.Error(err)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("set socket option: SO_REUSEADDR: %w", err)}
	}

	if err = syscall.SetsockoptInt(fileDescriptor, syscall.SOL_IP, syscall.IP_TRANSPARENT, 1); err != nil {
		if err := syscall.Close(fileDescriptor); err != nil {
			klog.Errorf("fileDescriptor %v Close err: %v", fileDescriptor, err)
		}
		klog.Error(err)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("set socket option: IP_TRANSPARENT: %w", err)}
	}

	if err = syscall.SetNonblock(fileDescriptor, isnonblocking); err != nil {
		if err := syscall.Close(fileDescriptor); err != nil {
			klog.Errorf("fileDescriptor %v Close err: %v", fileDescriptor, err)
		}
		klog.Error(err)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("set socket option: SO_NONBLOCK: %w", err)}
	}

	if !dontAssumeRemote {
		if err = syscall.Bind(fileDescriptor, localSocketAddress); err != nil {
			if err := syscall.Close(fileDescriptor); err != nil {
				klog.Errorf("fileDescriptor %v Close err: %v", fileDescriptor, err)
			}
			klog.Error(err)
			return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("socket bind: %w", err)}
		}
	}

	if err = syscall.Connect(fileDescriptor, remoteSocketAddress); err != nil && !strings.Contains(err.Error(), "operation now in progress") {
		if err := syscall.Close(fileDescriptor); err != nil {
			klog.Errorf("fileDescriptor %v Close err: %v", fileDescriptor, err)
		}
		klog.Error(err)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("socket connect: %w", err)}
	}

	fdFile := os.NewFile(uintptr(fileDescriptor), "net-tcp-dial-"+raddr.String()) // #nosec G115
	defer func() {
		if err := fdFile.Close(); err != nil {
			klog.Errorf("fdFile %v Close err: %v", fdFile, err)
		}
	}()

	remoteConn, err := net.FileConn(fdFile)
	if err != nil {
		if err := syscall.Close(fileDescriptor); err != nil {
			klog.Errorf("fileDescriptor %v Close err: %v", fileDescriptor, err)
		}
		klog.Error(err)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("convert file descriptor to connection: %w", err)}
	}

	return remoteConn.(*net.TCPConn), nil
}
