//go:build !unix

package executor

// processExists always returns true on non-Unix platforms to avoid
// incorrectly removing containers when process existence cannot be checked.
func processExists(_ int) bool {
	return true
}
