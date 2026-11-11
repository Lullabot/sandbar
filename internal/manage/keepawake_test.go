package manage

import "testing"

// StayAwake takes no real hold under `go test`, and its release is safe to
// call twice.
func TestStayAwakeIsInertUnderTest(t *testing.T) {
	release := StayAwake("test")
	release()
	release()
}
