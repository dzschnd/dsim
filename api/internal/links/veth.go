package links

import (
	"fmt"
	"os"
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
// identified by sandboxKeyA / sandboxKeyB (the Docker SandboxKey path, e.g.
// /var/run/docker/netns/<id>). Requires the containers to be running.
//
// Requirements: the process must have CAP_NET_ADMIN and CAP_SYS_ADMIN, and the
// /var/run/docker/netns directory must be visible inside the app container.
func CreateVethPair(nameA, nameB, sandboxKeyA, sandboxKeyB string) error {
	// Open B's netns first so we can place end B there at creation time.
	// This means end B never touches the current netns: a crash after LinkAdd
	// leaves only end A here, which the kernel cleans up automatically when
	// container B stops (peer destruction crosses namespace boundaries).
	nsB, err := os.Open(sandboxKeyB)
	if err != nil {
		return fmt.Errorf("open netns for container B (%s): %w", sandboxKeyB, err)
	}
	defer nsB.Close()

	veth := &netlink.Veth{
		LinkAttrs:     netlink.LinkAttrs{Name: nameA},
		PeerName:      nameB,
		PeerNamespace: netlink.NsFd(nsB.Fd()),
	}
	if err := netlink.LinkAdd(veth); err != nil {
		return fmt.Errorf("create veth pair %s/%s: %w", nameA, nameB, err)
	}

	// Only end A is in the current netns now; look it up and move it.
	linkA, err := netlink.LinkByName(nameA)
	if err != nil {
		_ = netlink.LinkDel(linkA)
		return fmt.Errorf("lookup veth %s: %w", nameA, err)
	}

	nsA, err := os.Open(sandboxKeyA)
	if err != nil {
		_ = netlink.LinkDel(linkA)
		return fmt.Errorf("open netns for container A (%s): %w", sandboxKeyA, err)
	}
	defer nsA.Close()

	if err := netlink.LinkSetNsFd(linkA, int(nsA.Fd())); err != nil {
		_ = netlink.LinkDel(linkA)
		return fmt.Errorf("move veth %s into container A netns: %w", nameA, err)
	}

	return nil
}
