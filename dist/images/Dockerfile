# syntax=docker/dockerfile:1
ARG VERSION=latest
ARG BASE_TAG=$VERSION
FROM kubeovn/kube-ovn-base:$BASE_TAG AS setcap

COPY *.sh /fabric/
COPY kubectl-ko /fabric/kubectl-ko
COPY 01-fabric.conflist /fabric/01-fabric.conflist

COPY fabric /fabric/fabric
COPY fabric-cmd /fabric/fabric-cmd
COPY fabric-bfdd-supervisor /fabric/fabric-bfdd-supervisor
COPY fabric-daemon /fabric/fabric-daemon
COPY fabric-controller /fabric/fabric-controller
RUN ln -s /fabric/fabric-cmd /fabric/fabric-monitor && \
    ln -s /fabric/fabric-cmd /fabric/fabric-webhook && \
    ln -s /fabric/fabric-cmd /fabric/fabric-leader-checker && \
    ln -s /fabric/fabric-cmd /fabric/fabric-ic-controller && \
    ln -s /fabric/fabric-cmd /fabric/fabric-frr && \
    ln -s /fabric/fabric-controller /fabric/fabric-pinger && \
    setcap CAP_NET_BIND_SERVICE+eip /fabric/fabric-cmd && \
    setcap CAP_NET_RAW,CAP_NET_BIND_SERVICE+eip /fabric/fabric-controller && \
    setcap CAP_NET_ADMIN,CAP_NET_RAW,CAP_NET_BIND_SERVICE,CAP_SYS_ADMIN+eip /fabric/fabric-daemon

FROM kubeovn/kube-ovn-base:$BASE_TAG

COPY --chmod=0644 logrotate/* /etc/logrotate.d/
COPY grace_stop_ovn_controller /usr/share/ovn/scripts/grace_stop_ovn_controller

COPY --from=setcap /fabric /fabric
RUN /fabric/iptables-wrapper-installer.sh --no-sanity-check && \
    if [ ! -x /usr/bin/valgrind ]; then setcap CAP_NET_ADMIN+eip "$(readlink -f /usr/bin/ovn-controller)"; fi

WORKDIR /fabric
