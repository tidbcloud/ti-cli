//go:build ti_test_controls

package fs

import (
	"os"

	"github.com/tidbcloud/ti-cli/internal/version"
)

// testControlsAllowed gates the fake mount-readiness control. It is a variable
// so tests can turn the controls on.
var testControlsAllowed = func() bool { return version.Current().TestControlsAllowed() }

// testFakeMountReady allows black-box e2e runs against the fake ti-drive9
// companion to pass mount evidence checks. The fake companion only creates a
// plain directory instead of a real kernel mount. Like the other TI_TEST_*
// controls it is hidden test configuration, never documented for users. It is
// compiled only with the ti_test_controls build tag, which make build sets and
// release builds don't, and it needs a local build plus
// TI_ALLOW_TEST_ENDPOINTS=1.
func testFakeMountReady() bool {
	return testControlsAllowed() && os.Getenv("TI_TEST_FAKE_MOUNT_READY") == "1"
}
