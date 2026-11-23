package sandbox

import (
	"errors"
	"syscall"
	"testing"
)

// fakeKernel stands in for the Landlock syscalls so the startup policy can
// be tested without restricting the test process.
type fakeKernel struct {
	abi         int
	probeErr    error
	restrictErr error
	restricted  bool
}

func (k *fakeKernel) probe() (int, error) { return k.abi, k.probeErr }

func (k *fakeKernel) restrict() error {
	k.restricted = true
	return k.restrictErr
}

func TestApplyEnforcesWhenSupported(t *testing.T) {
	for _, require := range []bool{false, true} {
		k := &fakeKernel{abi: 4}
		st, err := apply(Options{Require: require}, k.probe, k.restrict)
		if err != nil {
			t.Fatalf("require=%v: apply: %v", require, err)
		}
		if !k.restricted || st != (Status{ABI: 4, Enforced: true}) {
			t.Fatalf("require=%v: status=%+v restricted=%v, want enforced ABI 4", require, st, k.restricted)
		}
	}
}

func TestApplyUnsupportedBestEffortContinues(t *testing.T) {
	for name, k := range map[string]*fakeKernel{
		"ENOSYS":     {probeErr: syscall.ENOSYS},
		"EOPNOTSUPP": {probeErr: syscall.EOPNOTSUPP},
		"abi zero":   {abi: 0},
	} {
		st, err := apply(Options{Require: false}, k.probe, k.restrict)
		if err != nil {
			t.Fatalf("%s: apply = %v, want nil (best effort)", name, err)
		}
		if st.Enforced || k.restricted {
			t.Fatalf("%s: status=%+v restricted=%v, want not enforced and no restrict call", name, st, k.restricted)
		}
	}
}

func TestApplyUnsupportedRequiredAborts(t *testing.T) {
	k := &fakeKernel{probeErr: syscall.ENOSYS}
	_, err := apply(Options{Require: true}, k.probe, k.restrict)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("apply = %v, want ErrUnavailable", err)
	}
	if k.restricted {
		t.Fatal("restrict called although Landlock is unavailable")
	}
}

// A supported kernel that fails to enforce is a broken environment, not a
// missing feature: fail closed even in best-effort mode.
func TestApplyRestrictFailureIsFatal(t *testing.T) {
	for _, require := range []bool{false, true} {
		k := &fakeKernel{abi: 4, restrictErr: syscall.EINVAL}
		st, err := apply(Options{Require: require}, k.probe, k.restrict)
		if err == nil {
			t.Fatalf("require=%v: apply = nil, want error", require)
		}
		if st.Enforced {
			t.Fatalf("require=%v: status reports enforced after failure", require)
		}
	}
}
