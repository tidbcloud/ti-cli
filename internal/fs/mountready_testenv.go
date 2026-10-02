package fs

import "os"

// testFakeMountReady allows black-box e2e runs against the fake ti-drive9
// companion to pass mount evidence checks. The fake companion only creates a
// plain directory instead of a real kernel mount. Like the other TI_TEST_*
// controls it is hidden test configuration, never documented for users, and
// requires the explicit test-endpoint opt-in.
func testFakeMountReady() bool {
	return os.Getenv("TI_ALLOW_TEST_ENDPOINTS") == "1" && os.Getenv("TI_TEST_FAKE_MOUNT_READY") == "1"
}
