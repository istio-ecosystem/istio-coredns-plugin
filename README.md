# istio-coredns-plugin

CoreDNS gRPC plugin to serve DNS records out of Istio ServiceEntries.

The plugin runs as a separate container in the CoreDNS pod, serving DNS A
records over gRPC.
