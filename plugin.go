package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	dnsapi "github.com/rshriram/istio-coredns-plugin/api"

	networking "istio.io/api/networking/v1alpha3"
	"istio.io/istio/pilot/pkg/config/kube/crd"
	"istio.io/istio/pilot/pkg/model"
	"istio.io/istio/pilot/pkg/serviceregistry/kube"

	"google.golang.org/grpc"
)

type IstioServiceEntries struct {
	configStore model.IstioConfigStore
	mapMutex    sync.RWMutex
	stop        chan struct{}
	dnsEntries  map[string][]net.IP
}

func main() {
	// This is not working. Only in-cluster config works
	kubeconfig := flag.String("kubeconfig", "", "path to kube config")
	kubecontext := flag.String("context", "", "kube context to use")
	flag.Parse()

	h, err := NewIstioHandle(*kubeconfig, *kubecontext)
	if err != nil {
		log.Fatalf("Failed to initialize Istio CRD watcher: %v", err)
	}

	h.readServiceEntries()
	stop := make(chan bool)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				h.readServiceEntries()
			}
		}
	}()

	// start server
	listener, err := net.Listen("tcp", ":8053")
	if err != nil {
		log.Fatalf("Failed to start grpc server: %v", err)
	}
	grpcServer := grpc.NewServer()

	dnsapi.RegisterDnsServiceServer(grpcServer, h)
	grpcServer.Serve(listener)
	close(h.stop)
}

func (h *IstioServiceEntries) readServiceEntries() {
	//log.Printf("Reading service entries at %v\n", time.Now())
	dnsEntries := make(map[string][]net.IP)
	serviceEntries := h.configStore.ServiceEntries()
	//log.Printf("Have %d service entries\n", len(serviceEntries))
	for _, e := range serviceEntries {
		entry := e.Spec.(*networking.ServiceEntry)
		if errs := model.ValidateServiceEntry(e.Name, e.Namespace, entry); errs != nil {
			// log.Printf("Ignoring invalid service entry: %s.%s - %v\n", e.Name, e.Namespace, errs)
			// ignore invalid service entries
			continue
		}

		if entry.Resolution == networking.ServiceEntry_NONE || len(entry.Addresses) == 0 {
			// NO DNS based service discovery for service entries
			// that specify NONE as the resolution. NONE implies
			// that Istio should use the IP provided by the caller
			continue
		}

		vips := convertToVIPs(entry.Addresses)
		if len(vips) == 0 {
			continue
		}

		for _, host := range entry.Hosts {
			key := fmt.Sprintf("%s.", host)
			if strings.Contains(host, "*") {
				// Validation will ensure that the host is of the form *.foo.com
				parts := strings.SplitN(host, ".", 2)
				// Prefix wildcards with a . so that we can distinguish these entries in the map
				key = fmt.Sprintf(".%s.", parts[1])
			}
			dnsEntries[key] = vips
		}
	}
	h.mapMutex.Lock()
	h.dnsEntries = make(map[string][]net.IP)
	for k, v := range dnsEntries {
		// log.Printf("adding DNS mapping: %s->%v\n", k, v)
		h.dnsEntries[k] = v
	}
	h.mapMutex.Unlock()
	//log.Printf("Found %d service entries and have %v\n", len(serviceEntries), h.dnsEntries)
}

func convertToVIPs(addresses []string) []net.IP {
	vips := make([]net.IP, 0)

	for _, address := range addresses {
		// check if its CIDR.  If so, reject the address unless its /32 CIDR
		if strings.Contains(address, "/") {
			if ip, network, err := net.ParseCIDR(address); err != nil {
				ones, bits := network.Mask.Size()
				if ones == bits {
					// its a full mask (e.g., /32). Effectively an IP
					vips = append(vips, ip)
				}
			}
		} else {
			if ip := net.ParseIP(address); ip != nil {
				vips = append(vips, ip)
			}
		}
	}

	return vips
}

// code based on https://github.com/ahmetb/coredns-grpc-backend-sample
func (h *IstioServiceEntries) Query(ctx context.Context, in *dnsapi.DnsPacket) (*dnsapi.DnsPacket, error) {
	request := new(dns.Msg)
	if err := request.Unpack(in.Msg); err != nil {
		return nil, fmt.Errorf("failed to unmarshall dns query: %v", err)
	}

	response := new(dns.Msg)
	response.SetReply(request)
	response.Authoritative = true

	//log.Println("DNS query ", request)
	for _, q := range request.Question {
		switch q.Qtype {
		case dns.TypeA:
			var vips []net.IP
			//log.Printf("Query A record: %s->%v\n", q.Name, q)
			h.mapMutex.RLock()
			//log.Printf("DNS map: %v\n", h.dnsEntries)
			if h.dnsEntries != nil {
				vips = h.dnsEntries[q.Name]
				if vips == nil {
					// check for wildcard format
					// Split name into pieces by . (remember that DNS queries have dot in the end as well)
					// Check for each smaller variant of the name, until we have
					pieces := strings.Split(q.Name, ".")
					pieces = pieces[1:]
					for ; len(pieces) > 2; pieces = pieces[1:] {
						if vips = h.dnsEntries[fmt.Sprintf(".%s", strings.Join(pieces, "."))]; vips != nil {
							break
						}
					}
				}
			}
			h.mapMutex.RUnlock()
			if vips != nil {
				//log.Printf("Found %s->%v\n", q.Name, ip)
				response.Answer = a(q.Name, vips)
			}
			//default:
			//	log.Printf("Unknown query type: %v\n", q)
		}
	}
	if len(response.Answer) == 0 {
		//log.Println("Could not find the service requested")
		response.Rcode = dns.RcodeNameError
	}

	out, err := response.Pack()
	if err != nil {
		return nil, fmt.Errorf("failed to mashall dns response: %v", err)
	}
	return &dnsapi.DnsPacket{Msg: out}, nil
}

// Name implements the plugin.Handle interface.
func (h *IstioServiceEntries) Name() string { return "istio" }

// a takes a slice of net.IPs and returns a slice of A RRs.
func a(zone string, ips []net.IP) []dns.RR {
	answers := []dns.RR{}
	for _, ip := range ips {
		r := new(dns.A)
		r.Hdr = dns.RR_Header{Name: zone, Rrtype: dns.TypeA,
			Class: dns.ClassINET, Ttl: 3600}
		r.A = ip
		answers = append(answers, r)
	}
	return answers
}

func NewIstioHandle(kubeconfig string, context string) (*IstioServiceEntries, error) {
	var h = &IstioServiceEntries{}
	istioControllerOptions := kube.ControllerOptions{
		WatchedNamespace: "",
		ResyncPeriod:     60 * time.Second,
		DomainSuffix:     "cluster.local",
	}
	descriptors := model.ConfigDescriptor{
		model.ServiceEntry,
	}
	configClient, err := crd.NewClient(kubeconfig, context, descriptors, istioControllerOptions.DomainSuffix)

	if err != nil {
		return nil, err
	}

	configController := crd.NewController(configClient, istioControllerOptions)
	go configController.Run(h.stop)
	h.configStore = model.MakeIstioStore(configController)
	return h, nil
}
