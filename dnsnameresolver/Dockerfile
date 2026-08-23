FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder

ARG TARGETARCH
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY

# Set build arguments first
ARG COREDNS_VERSION=v1.12.3
ARG PLUGIN_VERSION=dev

# Set the GOPATH and create directories
WORKDIR /go/src

# Clone CoreDNS and build once to cache dependencies (this layer will be cached)
RUN git clone https://github.com/coredns/coredns.git /coredns && \
    cd /coredns && \
    git checkout $COREDNS_VERSION && \
    go mod download && \
    go generate && \
    CGO_ENABLED=0 GOARCH=$TARGETARCH go build -o /coredns/coredns .

# Copy the fabric repo so the plugin's replace directive resolves (this layer changes when code changes)
COPY . /go/src/fabric/

# Build CoreDNS with the dnsnameresolver plugin (reusing cached dependencies)
RUN cd /coredns && \
    sed -i '/root:root/i dnsnameresolver:github.com/kubeovn/dnsnameresolver' plugin.cfg && \
    go mod edit -replace k8s.io/api=k8s.io/api@v0.36.3 && \
    go mod edit -replace k8s.io/apimachinery=k8s.io/apimachinery@v0.36.3 && \
    go mod edit -replace k8s.io/client-go=k8s.io/client-go@v0.36.3 && \
    go mod edit -replace k8s.io/kube-openapi=k8s.io/kube-openapi@v0.0.0-20250910181357-589584f1c912 && \
    go mod edit -replace github.com/kubeovn/kube-ovn=/go/src/fabric && \
    go mod edit -replace github.com/kubeovn/dnsnameresolver=/go/src/fabric/dnsnameresolver && \
    go mod edit -require github.com/kubeovn/dnsnameresolver@v0.0.0 && \
    go mod tidy && \
    go get github.com/kubeovn/dnsnameresolver && \
    go mod tidy && \
    go generate && \
    go mod tidy && \
    CGO_ENABLED=0 GOARCH=$TARGETARCH go build -ldflags "-X github.com/kubeovn/dnsnameresolver.Version=$PLUGIN_VERSION" -o /coredns/coredns .

# Create the final image with CoreDNS binary
FROM debian:bookworm

COPY --from=builder /coredns/coredns /usr/bin/coredns

RUN apt-get update && apt-get upgrade -y
WORKDIR /

ENTRYPOINT ["/usr/bin/coredns"]