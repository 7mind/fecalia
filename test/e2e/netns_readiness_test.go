//go:build e2e

package e2e

import "testing"

// Regression: D126 — the holder's /proc namespace entry exists before unshare
// necessarily publishes the child's distinct network namespace.
func TestWaitForNetnsRequiresDistinctChildNamespaceIdentity(t *testing.T) {
	const (
		currentPath = "/proc/self/ns/net"
		childPath   = "/proc/42/ns/net"
		currentID   = "net:[100]"
		childID     = "net:[200]"
	)
	childReads := 0
	readIdentity := func(path string) (string, error) {
		switch path {
		case currentPath:
			return currentID, nil
		case childPath:
			childReads++
			if childReads == 1 {
				return currentID, nil
			}
			return childID, nil
		default:
			t.Fatalf("unexpected identity path %q", path)
			return "", nil
		}
	}

	got, err := waitForNetnsIdentity(readIdentity, childPath, 3, func() {})
	if err != nil {
		t.Fatalf("waitForNetnsIdentity: %v", err)
	}
	if got.current == got.child {
		t.Fatalf(
			"readiness returned shared namespace identity %q after %d child read; want wait for %q",
			got.child,
			childReads,
			childID,
		)
	}
	if got.child != childID || childReads != 2 {
		t.Fatalf(
			"readiness = %+v after %d child reads, want child %q after 2",
			got,
			childReads,
			childID,
		)
	}
}
