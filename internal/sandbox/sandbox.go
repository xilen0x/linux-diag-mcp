// Package sandbox confines the process with Landlock before any tool runs.
//
// Policy: read-only access to /proc, /sys and /etc/os-release; no writes,
// no execution, no TCP/UDP bind or connect (ABI >= 4/10), and IPC scoping
// (ABI >= 6). Landlock cannot be lifted once applied and covers every
// thread of the process.
package sandbox

import (
	"errors"
	"fmt"

	"github.com/landlock-lsm/go-landlock/landlock"
	ll "github.com/landlock-lsm/go-landlock/landlock/syscall"
)

// ErrUnavailable is returned when Landlock is required but the kernel does
// not support it (not built in, or disabled at boot).
var ErrUnavailable = errors.New("landlock is not available on this kernel")

// Options controls how Apply reacts to a kernel without Landlock.
type Options struct {
	// Require turns "Landlock unavailable" into an error instead of a
	// warning-worthy Status.
	Require bool
}

// Status reports what was enforced.
type Status struct {
	// ABI is the Landlock ABI version of the running kernel (0 = none).
	ABI int
	// Enforced is true when the policy is active.
	Enforced bool
}

// Apply enforces the sandbox policy on the whole process.
//
// If Landlock is unavailable it returns a non-enforced Status and nil, unless
// opts.Require is set, in which case it returns ErrUnavailable. If Landlock is
// available but enforcement fails, it always returns an error (fail closed).
func Apply(opts Options) (Status, error) {
	return apply(opts, ll.LandlockGetABIVersion, restrict)
}

// apply holds the startup policy, with the kernel calls injected for tests.
func apply(opts Options, probe func() (int, error), enforce func() error) (Status, error) {
	abi, err := probe()
	if err != nil || abi < 1 {
		if opts.Require {
			if err != nil {
				return Status{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
			}
			return Status{}, fmt.Errorf("%w (abi=%d)", ErrUnavailable, abi)
		}
		return Status{}, nil
	}
	if err := enforce(); err != nil {
		return Status{ABI: abi}, fmt.Errorf("enforce landlock (abi=%d): %w", abi, err)
	}
	return Status{ABI: abi, Enforced: true}, nil
}

// Read-only rights, deliberately without AccessFSExecute (which
// landlock.RODirs/ROFiles would include). Directory-only rights such as
// ReadDir are rejected by the kernel on regular files.
const (
	readOnlyDir  = landlock.AccessFSSet(ll.AccessFSReadFile | ll.AccessFSReadDir)
	readOnlyFile = landlock.AccessFSSet(ll.AccessFSReadFile)
)

// restrict applies the policy with the highest ABI the kernel supports.
// Restrict (not RestrictPaths) also denies TCP/UDP and scopes IPC, since
// the server never needs the network.
func restrict() error {
	return landlock.V11.BestEffort().Restrict(
		landlock.PathAccess(readOnlyDir, "/proc", "/sys"),
		// Usually a symlink to /usr/lib/os-release: the rule binds to the
		// target inode. Minimal containers may not ship it.
		landlock.PathAccess(readOnlyFile, "/etc/os-release").IgnoreIfMissing(),
	)
}
