package links

import (
	"fmt"
	"strings"

	"github.com/vishvananda/netlink"
)

func vethShortID(linkID string) string {
	s := linkID
	if i := strings.IndexByte(s, '_'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 6 {
		s = s[:6]
	}
	return s
}

func VethNameA(linkID string) string { return "da_" + vethShortID(linkID) }
func VethNameB(linkID string) string { return "db_" + vethShortID(linkID) }

// keep unexported aliases for use within the links package
func vethNameA(linkID string) string { return VethNameA(linkID) }
func vethNameB(linkID string) string { return VethNameB(linkID) }

// CreateVethPair creates a veth pair and moves each end into the network namespace
// of the container identified by pidA / pidB (the container's init PID from
// ContainerInspect State.Pid). Requires the containers to be running.
//
// Requirements: the process must have CAP_NET_ADMIN and CAP_SYS_ADMIN, and
// /proc must be visible so the kernel can resolve /proc/<pid>/ns/net.
func CreateVethPair(nameA, nameB string, pidA, pidB int) error {
	// Create end A in the current netns; end B goes directly into container B's
	// netns via IFLA_NET_NS_PID, which the kernel resolves through /proc/<pid>/ns/net.
	veth := &netlink.Veth{
		LinkAttrs:     netlink.LinkAttrs{Name: nameA},
		PeerName:      nameB,
		PeerNamespace: netlink.NsPid(pidB),
	}
	if err := netlink.LinkAdd(veth); err != nil {
		return fmt.Errorf("create veth pair %s/%s: %w", nameA, nameB, err)
	}

	// End A is still in the current netns; look it up and move it into container A.
	linkA, err := netlink.LinkByName(nameA)
	if err != nil {
		_ = netlink.LinkDel(linkA)
		return fmt.Errorf("lookup veth %s: %w", nameA, err)
	}

	if err := netlink.LinkSetNsPid(linkA, pidA); err != nil {
		_ = netlink.LinkDel(linkA)
		return fmt.Errorf("move veth %s into container A netns: %w", nameA, err)
	}

	return nil
}
