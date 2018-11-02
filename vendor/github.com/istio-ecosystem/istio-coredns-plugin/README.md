# istio-coredns-plugin

CoreDNS gRPC plugin to serve DNS records out of Istio ServiceEntries.

The plugin runs as a separate container in the CoreDNS pod, serving DNS A
records over gRPC to CoreDNS.

Hosts in service entries which also contain addresses will resolve to those
addresses, as long as they're host addresses not CIDR ranges.

Service entries without addresses will by default not resolve, unless the
--default-address flag is given, in which case that address will be used
for address-less service entries.

Wildcard hosts in the service entries will also resolve appropriately.
E.g., consider the following service entry:

```yaml
apiVersion: networking.istio.io/v1alpha3
kind: ServiceEntry
spec:
  hosts:
  - *.google.com
  addresses:
  - 17.17.17.17
  - 9.9.9.9
  resolution: STATIC
  endpoints:
  - ...
```

A query against the coreDNS pod would return the following:

```bash
$ dig +short @<coreDNSIP> A maps.google.com
17.17.17.17
9.9.9.9

$ dig +short @<coreDNSIP> A mail.google.com
17.17.17.17
9.9.9.9

$ dig +short @<coreDNSIP> A google.com
 # no response
```

## Usage

Deploy the core-DNS service in the istio-system namespace

```
kubectl apply -f coredns.yaml
```

Update the kube-dns config map to point to this coredns service as the
upstream DNS service for the `*.global` domain. You will have to find out
the cluster IP of coredns service and update the config map (or write a
controller for this purpose!).

E.g.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kube-dns
  namespace: kube-system
data:
  stubDomains: |
    {"global": ["10.2.3.4"]}
```
