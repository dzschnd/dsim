package store

import (
	"fmt"
	"net/netip"
	"sync"
)

type SubnetAllocator struct {
	mu          sync.Mutex
	base        netip.Prefix
	allocBits   int
	blockSize   uint32
	subnetCount uint32
	used        map[string]struct{}
}

func NewSubnetAllocator(baseCIDR string, allocBits int) (*SubnetAllocator, error) {
	base, err := netip.ParsePrefix(baseCIDR)
	if err != nil {
		return nil, fmt.Errorf("parse base subnet: %w", err)
	}
	base = base.Masked()
	if !base.Addr().Is4() {
		return nil, fmt.Errorf("subnet allocator only supports ipv4")
	}
	if allocBits < base.Bits() || allocBits > 32 {
		return nil, fmt.Errorf("invalid allocated prefix length %d for base %s", allocBits, base)
	}

	blockSize := uint32(1) << uint32(32-allocBits)
	subnetCount := uint32(1) << uint32(allocBits-base.Bits())

	return &SubnetAllocator{
		base:        base,
		allocBits:   allocBits,
		blockSize:   blockSize,
		subnetCount: subnetCount,
		used:        make(map[string]struct{}),
	}, nil
}

func (a *SubnetAllocator) Allocate() (netip.Prefix, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	baseUint := ipv4ToUint32(a.base.Addr())
	for index := uint32(0); index < a.subnetCount; index++ {
		addr := uint32ToIPv4(baseUint + index*a.blockSize)
		subnet := netip.PrefixFrom(addr, a.allocBits).Masked()
		if _, used := a.used[subnet.String()]; used {
			continue
		}
		a.used[subnet.String()] = struct{}{}
		return subnet, nil
	}

	return netip.Prefix{}, fmt.Errorf("subnet allocator exhausted for %s/%d", a.base.Addr(), a.allocBits)
}

func (a *SubnetAllocator) Release(subnet netip.Prefix) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.used, subnet.Masked().String())
}

func (a *SubnetAllocator) ReleaseString(cidr string) {
	subnet, err := netip.ParsePrefix(cidr)
	if err != nil {
		return
	}
	a.Release(subnet)
}

func (a *SubnetAllocator) ReserveOverlapping(other netip.Prefix) int {
	a.mu.Lock()
	defer a.mu.Unlock()

	other = other.Masked()
	if !other.Addr().Is4() || !a.base.Overlaps(other) {
		return 0
	}

	baseUint := ipv4ToUint32(a.base.Addr())
	reserved := 0
	for index := uint32(0); index < a.subnetCount; index++ {
		addr := uint32ToIPv4(baseUint + index*a.blockSize)
		subnet := netip.PrefixFrom(addr, a.allocBits).Masked()
		if !subnet.Overlaps(other) {
			continue
		}
		if _, used := a.used[subnet.String()]; used {
			continue
		}
		a.used[subnet.String()] = struct{}{}
		reserved++
	}

	return reserved
}

func GatewayAddr(subnet netip.Prefix) (netip.Addr, error) {
	subnet = subnet.Masked()
	if !subnet.Addr().Is4() {
		return netip.Addr{}, fmt.Errorf("gateway address only supported for ipv4 subnets")
	}

	gateway := subnet.Addr().Next()
	if !subnet.Contains(gateway) {
		return netip.Addr{}, fmt.Errorf("subnet %s has no usable gateway address", subnet)
	}
	return gateway, nil
}

func ipv4ToUint32(addr netip.Addr) uint32 {
	bytes := addr.As4()
	return uint32(bytes[0])<<24 | uint32(bytes[1])<<16 | uint32(bytes[2])<<8 | uint32(bytes[3])
}

func uint32ToIPv4(value uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{
		byte(value >> 24),
		byte(value >> 16),
		byte(value >> 8),
		byte(value),
	})
}
