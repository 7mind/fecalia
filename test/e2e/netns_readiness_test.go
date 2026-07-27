//go:build e2e

package e2e

import (
	"errors"
	"strings"
	"testing"
)

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

func TestWaitForNetnsReportsPersistentSharedIdentity(t *testing.T) {
	const (
		childPath = "/proc/42/ns/net"
		currentID = "net:[100]"
	)
	readIdentity := func(string) (string, error) {
		return currentID, nil
	}

	got, err := waitForNetnsIdentity(readIdentity, childPath, 2, func() {})
	if err == nil {
		t.Fatalf("readiness accepted persistent shared identities: %+v", got)
	}
	for _, want := range []string{childPath, currentID, "after 2 attempts"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("readiness error %q missing %q", err, want)
		}
	}
}

func TestWaitForNetnsReportsMissingChild(t *testing.T) {
	const (
		currentPath = "/proc/self/ns/net"
		childPath   = "/proc/42/ns/net"
		currentID   = "net:[100]"
	)
	readIdentity := func(path string) (string, error) {
		if path == currentPath {
			return currentID, nil
		}
		return "", errors.New("holder exited")
	}

	_, err := waitForNetnsIdentity(readIdentity, childPath, 2, func() {})
	if err == nil {
		t.Fatal("readiness accepted a missing child namespace")
	}
	for _, want := range []string{childPath, currentID, "holder exited"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("readiness error %q missing %q", err, want)
		}
	}
}
