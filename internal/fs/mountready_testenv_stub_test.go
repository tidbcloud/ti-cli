//go:build !ti_test_controls

package fs

import "testing"

func TestFakeMountReadyIsCompiledOutWithoutTestControlsTag(t *testing.T) {
	t.Setenv("TI_ALLOW_TEST_ENDPOINTS", "1")
	t.Setenv("TI_TEST_FAKE_MOUNT_READY", "1")
	if testFakeMountReady() {
		t.Fatal("the fake mount bypass must be off in builds without the ti_test_controls tag")
	}
}
