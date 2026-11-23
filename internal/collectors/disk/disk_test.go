package disk_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/xilen0x/linux-diag-mcp/internal/collectors/disk"
)

// fakeStatfs serves canned results and records which paths were queried.
type fakeStatfs struct {
	mu      sync.Mutex
	results map[string]disk.Stat
	errs    map[string]error
	called  []string
}

func (f *fakeStatfs) stat(path string) (disk.Stat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called = append(f.called, path)
	if err := f.errs[path]; err != nil {
		return disk.Stat{}, err
	}
	return f.results[path], nil
}

func TestCollectFixture(t *testing.T) {
	fake := &fakeStatfs{
		results: map[string]disk.Stat{
			"/":            {BlockSize: 4096, Blocks: 1000, BlocksFree: 400, BlocksAvail: 300, Inodes: 100, InodesFree: 25},
			"/home":        {BlockSize: 4096}, // zero blocks: skipped like a pseudo-fs
			"/mnt/my disk": {BlockSize: 1024, Blocks: 10, BlocksFree: 10, BlocksAvail: 10},
			"/mnt/nfs":     {BlockSize: 512, Blocks: 4, BlocksFree: 0, BlocksAvail: 0, Inodes: 0},
		},
		errs: map[string]error{"/boot": syscall.EACCES},
	}
	got, err := disk.Collect(context.Background(), os.DirFS("testdata"), fake.stat)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	want := disk.Usage{
		Filesystems: []disk.Filesystem{
			{MountPoint: "/", Device: "/dev/sda3", FSType: "ext4",
				TotalBytes: 4096000, UsedBytes: 2457600, AvailableBytes: 1228800, UsedPercent: 66.67,
				InodesTotal: 100, InodesUsed: 75, InodesUsedPercent: 75},
			{MountPoint: "/mnt/my disk", Device: "/dev/sdb1", FSType: "ext4", ReadOnly: true,
				TotalBytes: 10240, UsedBytes: 0, AvailableBytes: 10240, UsedPercent: 0},
			{MountPoint: "/mnt/nfs", Device: "server:/export", FSType: "nfs4",
				TotalBytes: 2048, UsedBytes: 2048, AvailableBytes: 0, UsedPercent: 100},
		},
		Unavailable: []string{"/boot"},
	}
	if !slices.Equal(got.Filesystems, want.Filesystems) {
		t.Errorf("filesystems:\n got  %+v\n want %+v", got.Filesystems, want.Filesystems)
	}
	if !slices.Equal(got.Unavailable, want.Unavailable) {
		t.Errorf("unavailable = %v, want %v", got.Unavailable, want.Unavailable)
	}
	// Pseudo filesystems must be filtered before any statfs call.
	wantCalled := []string{"/", "/home", "/boot", "/mnt/my disk", "/mnt/nfs"}
	if !slices.Equal(fake.called, wantCalled) {
		t.Errorf("statfs called on %v, want %v", fake.called, wantCalled)
	}
}

func TestOvermountLastWins(t *testing.T) {
	mounts := "/dev/a /data ext4 rw 0 0\n/dev/b /data xfs rw 0 0\n"
	fake := &fakeStatfs{results: map[string]disk.Stat{"/data": {BlockSize: 1, Blocks: 1}}}
	got, err := disk.Collect(context.Background(), fstest.MapFS{"mounts": {Data: []byte(mounts)}}, fake.stat)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Filesystems) != 1 || got.Filesystems[0].Device != "/dev/b" {
		t.Fatalf("got %+v, want only the top mount /dev/b", got.Filesystems)
	}
}

func TestSizeOverflowSaturates(t *testing.T) {
	mounts := "/dev/a /big ext4 rw 0 0\n"
	huge := disk.Stat{BlockSize: 1 << 20, Blocks: 1 << 50, BlocksFree: 0, BlocksAvail: 0}
	fake := &fakeStatfs{results: map[string]disk.Stat{"/big": huge}}
	got, err := disk.Collect(context.Background(), fstest.MapFS{"mounts": {Data: []byte(mounts)}}, fake.stat)
	if err != nil {
		t.Fatal(err)
	}
	if got.Filesystems[0].TotalBytes != ^uint64(0) {
		t.Fatalf("total = %d, want saturated MaxUint64", got.Filesystems[0].TotalBytes)
	}
}

// statfs on a dead network mount can block forever; Collect must return
// when the context ends instead of hanging the tool call.
func TestCollectDoesNotHangOnBlockedStatfs(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	hang := func(string) (disk.Stat, error) { <-block; return disk.Stat{}, nil }

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := disk.Collect(ctx, fstest.MapFS{"mounts": {Data: []byte("srv:/x /mnt/nfs nfs4 rw 0 0\n")}}, hang)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("Collect did not return promptly")
	}
}

func TestCollectRejectsMalformed(t *testing.T) {
	for name, data := range map[string]string{
		"too few fields":   "/dev/a /x\n",
		"relative mount":   "/dev/a relative ext4 rw 0 0\n",
		"bad escape":       "/dev/a /x\\09 ext4 rw 0 0\n",
		"truncated escape": "/dev/a /x\\04 ext4 rw 0 0\n",
		"escape overflow":  "/dev/a /x\\777 ext4 rw 0 0\n",
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeStatfs{}
			_, err := disk.Collect(context.Background(), fstest.MapFS{"mounts": {Data: []byte(data)}}, fake.stat)
			if err == nil {
				t.Fatal("Collect succeeded, want error")
			}
		})
	}
}
