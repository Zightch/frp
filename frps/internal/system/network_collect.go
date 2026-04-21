package system

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

type netInterfaceCollector struct {
	platform string
}

type discoveredInterface struct {
	Name  string
	Index int
	Addrs []net.Addr
}

func (c netInterfaceCollector) Platform() string {
	return c.platform
}

func (c netInterfaceCollector) Collect() (Snapshot, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return Snapshot{}, fmt.Errorf("list network interfaces: %w", err)
	}

	discovered := make([]discoveredInterface, 0, len(interfaces))
	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return Snapshot{}, fmt.Errorf("list interface %q addresses: %w", iface.Name, err)
		}
		discovered = append(discovered, discoveredInterface{
			Name:  iface.Name,
			Index: iface.Index,
			Addrs: addrs,
		})
	}

	return snapshotFromDiscoveredInterfaces(c.platform, time.Now().UTC(), discovered), nil
}

func snapshotFromDiscoveredInterfaces(platform string, capturedAt time.Time, discovered []discoveredInterface) Snapshot {
	sorted := append([]discoveredInterface(nil), discovered...)
	sort.Slice(sorted, func(left, right int) bool {
		if sorted[left].Index != sorted[right].Index {
			return sorted[left].Index < sorted[right].Index
		}
		return strings.Compare(sorted[left].Name, sorted[right].Name) < 0
	})

	snapshot := Snapshot{
		Platform:   platform,
		CapturedAt: capturedAt,
		Interfaces: make([]NetworkInterface, 0, len(sorted)),
	}

	available := make(map[string]IPAddress)
	for _, item := range sorted {
		next := NetworkInterface{
			Name:  strings.TrimSpace(item.Name),
			Index: item.Index,
		}

		seenOnInterface := make(map[string]IPAddress)
		for _, addr := range item.Addrs {
			ip, ok := normalizeAddr(addr)
			if !ok {
				continue
			}
			seenOnInterface[ip.Addr] = ip
			available[ip.Addr] = ip
		}

		if len(seenOnInterface) > 0 {
			next.IPs = make([]IPAddress, 0, len(seenOnInterface))
			for _, ip := range seenOnInterface {
				next.IPs = append(next.IPs, ip)
			}
			sortIPAddresses(next.IPs)
		}

		snapshot.Interfaces = append(snapshot.Interfaces, next)
	}

	if len(available) > 0 {
		snapshot.AvailableIPs = make([]IPAddress, 0, len(available))
		for _, ip := range available {
			snapshot.AvailableIPs = append(snapshot.AvailableIPs, ip)
		}
		sortIPAddresses(snapshot.AvailableIPs)
	}

	return snapshot
}

func normalizeAddr(addr net.Addr) (IPAddress, bool) {
	switch typed := addr.(type) {
	case *net.IPNet:
		return normalizeIP(typed.IP)
	case *net.IPAddr:
		return normalizeIP(typed.IP)
	default:
		return IPAddress{}, false
	}
}

func normalizeIP(raw net.IP) (IPAddress, bool) {
	if raw == nil {
		return IPAddress{}, false
	}
	if ipv4 := raw.To4(); ipv4 != nil {
		return IPAddress{Addr: ipv4.String(), Family: FamilyIPv4}, true
	}
	if ipv6 := raw.To16(); ipv6 != nil {
		return IPAddress{Addr: ipv6.String(), Family: FamilyIPv6}, true
	}
	return IPAddress{}, false
}

func sortIPAddresses(items []IPAddress) {
	sort.Slice(items, func(left, right int) bool {
		if items[left].Family != items[right].Family {
			return items[left].Family < items[right].Family
		}
		return strings.Compare(items[left].Addr, items[right].Addr) < 0
	})
}
