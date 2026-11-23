// Package disk reports usage of real (non-pseudo) mounted filesystems from
// /proc/mounts and statfs(2).
package disk

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math/bits"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/procio"
)

// Usage is the result of disk_usage.
type Usage struct {
	Filesystems []Filesystem `json:"filesystems"`
	// Unavailable lists mount points whose statfs call failed.
	Unavailable []string `json:"unavailable,omitempty" jsonschema:"mount points that could not be queried"`
}

// Filesystem is the usage of one mount point. Sizes are in bytes.
type Filesystem struct {
	MountPoint        string  `json:"mount_point"`
	Device            string  `json:"device"`
	FSType            string  `json:"fs_type"`
	ReadOnly          bool    `json:"read_only"`
	TotalBytes        uint64  `json:"total_bytes"`
	UsedBytes         uint64  `json:"used_bytes"`
	AvailableBytes    uint64  `json:"available_bytes" jsonschema:"space available to unprivileged users"`
	UsedPercent       float64 `json:"used_percent" jsonschema:"used / (used + available), as df computes it"`
	InodesTotal       uint64  `json:"inodes_total"`
	InodesUsed        uint64  `json:"inodes_used"`
	InodesUsedPercent float64 `json:"inodes_used_percent"`
}

// Stat is the subset of statfs(2) results used here.
type Stat struct {
	BlockSize, Blocks, BlocksFree, BlocksAvail, Inodes, InodesFree uint64
}

// StatFunc queries a mounted filesystem. Inject a fake in tests.
type StatFunc func(path string) (Stat, error)

// SysStatfs is the real StatFunc, backed by statfs(2). Landlock does not
// restrict statfs, so it works inside the sandbox.
func SysStatfs(path string) (Stat, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Stat{}, err
	}
	bsize := st.Frsize // fragment size is the unit of f_blocks
	if bsize <= 0 {
		bsize = st.Bsize
	}
	if bsize < 0 {
		bsize = 0
	}
	return Stat{
		BlockSize: uint64(bsize), Blocks: st.Blocks, BlocksFree: st.Bfree,
		BlocksAvail: st.Bavail, Inodes: st.Files, InodesFree: st.Ffree,
	}, nil
}

// pseudo lists filesystem types that hold no persistent user data, plus
// squashfs (snap/live images: read-only and always 100% full).
func pseudo(fstype string) bool {
	switch fstype {
	case "proc", "sysfs", "tmpfs", "devtmpfs", "devpts", "overlay", "cgroup", "cgroup2",
		"securityfs", "debugfs", "tracefs", "pstore", "bpf", "configfs", "fusectl",
		"mqueue", "hugetlbfs", "autofs", "binfmt_misc", "efivarfs", "nsfs", "ramfs",
		"rpc_pipefs", "nfsd", "selinuxfs", "squashfs", "fuse.portal", "fuse.gvfsd-fuse":
		return true
	}
	return false
}

type mount struct {
	device, point, fstype string
	readOnly              bool
}

// Collect reads "mounts" from procFS (rooted at /proc) and calls statfs on
// each real filesystem. statfs on a dead network mount can block forever,
// so the calls run in a worker goroutine and Collect returns ctx.Err() when
// ctx ends; the stuck syscall cannot be interrupted and is abandoned.
func Collect(ctx context.Context, procFS fs.FS, statfs StatFunc) (Usage, error) {
	mounts, err := readMounts(procFS)
	if err != nil {
		return Usage{}, err
	}
	type result struct {
		u   Usage
		err error
	}
	done := make(chan result, 1) // buffered: an abandoned worker never blocks
	go func() {
		// A panic in this goroutine is beyond any recover in the caller
		// and would kill the process.
		defer func() {
			if p := recover(); p != nil {
				done <- result{err: fmt.Errorf("statfs worker panicked: %v", p)}
			}
		}()
		done <- result{u: statAll(mounts, statfs)}
	}()
	select {
	case <-ctx.Done():
		return Usage{}, ctx.Err()
	case r := <-done:
		return r.u, r.err
	}
}

func statAll(mounts []mount, statfs StatFunc) Usage {
	u := Usage{Filesystems: []Filesystem{}}
	for _, m := range mounts {
		st, err := statfs(m.point)
		if err != nil {
			u.Unavailable = append(u.Unavailable, procio.CleanText(m.point))
			continue
		}
		if st.Blocks == 0 {
			continue // no capacity: pseudo filesystem not in the list
		}
		used := sat(st.Blocks-min(st.BlocksFree, st.Blocks), st.BlockSize)
		avail := sat(st.BlocksAvail, st.BlockSize)
		inodesUsed := st.Inodes - min(st.InodesFree, st.Inodes)
		u.Filesystems = append(u.Filesystems, Filesystem{
			MountPoint:        procio.CleanText(m.point),
			Device:            procio.CleanText(m.device),
			FSType:            procio.CleanText(m.fstype),
			ReadOnly:          m.readOnly,
			TotalBytes:        sat(st.Blocks, st.BlockSize),
			UsedBytes:         used,
			AvailableBytes:    avail,
			UsedPercent:       procio.Percent(used, used+min(avail, ^uint64(0)-used)),
			InodesTotal:       st.Inodes,
			InodesUsed:        inodesUsed,
			InodesUsedPercent: procio.Percent(inodesUsed, st.Inodes),
		})
	}
	return u
}

// sat multiplies, saturating at MaxUint64 instead of wrapping around.
func sat(a, b uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	if hi != 0 {
		return ^uint64(0)
	}
	return lo
}

// readMounts parses /proc/mounts, drops pseudo filesystems and keeps only
// the last mount per mount point (an overmount hides the ones below).
func readMounts(procFS fs.FS) ([]mount, error) {
	data, err := procio.ReadFile(procFS, "mounts", procio.MaxFileBytes)
	if err != nil {
		return nil, err
	}
	var all []mount
	index := map[string]int{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			return nil, errors.New("parse /proc/mounts: too few fields")
		}
		if pseudo(f[2]) {
			continue
		}
		point, err := unescape(f[1])
		if err != nil {
			return nil, fmt.Errorf("parse /proc/mounts: %w", err)
		}
		if !strings.HasPrefix(point, "/") {
			return nil, errors.New("parse /proc/mounts: mount point is not absolute")
		}
		dev, err := unescape(f[0])
		if err != nil {
			return nil, fmt.Errorf("parse /proc/mounts: %w", err)
		}
		m := mount{device: dev, point: point, fstype: f[2], readOnly: hasOption(f[3], "ro")}
		if i, ok := index[point]; ok {
			all[i] = m
			continue
		}
		index[point] = len(all)
		all = append(all, m)
	}
	return all, sc.Err()
}

// unescape decodes the octal escapes the kernel uses in /proc/mounts for
// space, tab, newline and backslash (e.g. "\040").
func unescape(s string) (string, error) {
	if !strings.Contains(s, `\`) {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		if i+4 > len(s) {
			return "", errors.New("truncated escape")
		}
		d := s[i+1 : i+4]
		if d[0] > '3' { // above \377 does not fit in a byte
			return "", errors.New("invalid escape")
		}
		var v byte
		for _, c := range []byte(d) {
			if c < '0' || c > '7' {
				return "", errors.New("invalid escape")
			}
			v = v<<3 | (c - '0')
		}
		b.WriteByte(v)
		i += 3
	}
	return b.String(), nil
}

func hasOption(opts, want string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
}
