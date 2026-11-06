//go:build !ti_test_controls

package fs

// testFakeMountReady is always false without the ti_test_controls build tag,
// so release builds never skip the mount evidence checks.
func testFakeMountReady() bool { return false }
