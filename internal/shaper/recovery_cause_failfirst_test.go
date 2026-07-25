package shaper

import (
	"context"
	"errors"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestRecoveryPredecessorFailurePreservesCauseAndTerminalByteConservation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{name: "generic", cause: errors.New("predecessor write failed")},
		{name: "EMSGSIZE", cause: syscall.EMSGSIZE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newFakeClock()
			writerStarted := make(chan struct{})
			failWriter := make(chan struct{})
			var startOnce sync.Once
			var failOnce sync.Once
			var shaped *Shaper
			var err error
			shaped, err = New(recoveryConfig(), clock, func([]byte) error {
				startOnce.Do(func() { close(writerStarted) })
				<-failWriter
				return tc.cause
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				failOnce.Do(func() { close(failWriter) })
				_ = shaped.Close()
			}()

			predecessor := make(chan error, 1)
			go func() {
				predecessor <- shaped.WriteBatch(
					context.Background(),
					ClassData,
					[][]byte{bytesOf(1, 100)},
				)
			}()
			waitChannel(t, writerStarted, "predecessor did not enter Lio")

			cutInstalled := make(chan struct{})
			var installOnce sync.Once
			abortCause := make(chan error, 1)
			recovery := make(chan error, 1)
			go func() {
				_, writeErr := shaped.WriteRecovery(
					context.Background(),
					[]Datagram{
						{Class: ClassData, Payload: bytesOf(2, 100)},
						{Class: ClassControl, Payload: bytesOf(3, 100)},
					},
					RecoveryControl{
						Enabled: true,
						InstallDeadline: func(time.Time) error {
							installOnce.Do(func() { close(cutInstalled) })
							return nil
						},
						ClearDeadline: func() error { return nil },
						Abort: func(cause error) {
							abortCause <- cause
							shaped.StopWithError(cause)
						},
					},
				)
				recovery <- writeErr
			}()
			waitChannel(t, cutInstalled, "recovery cut was not admitted")

			admitted, priorityDone, err := shaped.TryWritePriority(bytesOf(4, 50), nil)
			if err != nil || !admitted {
				t.Fatalf("post-cut priority admission = %v, %v", admitted, err)
			}
			admitted, postCutDone, err := shaped.TryWritePriority(bytesOf(5, 50), nil)
			if err != nil || !admitted {
				t.Fatalf("second post-cut priority admission = %v, %v", admitted, err)
			}
			waitFor(t, func() bool {
				return shaped.Snapshot().AcceptedBytes == 400
			})

			failOnce.Do(func() { close(failWriter) })
			for name, result := range map[string]<-chan error{
				"predecessor": predecessor,
				"recovery":    recovery,
				"priority":    priorityDone,
				"post-cut":    postCutDone,
				"abort":       abortCause,
			} {
				if got := waitResult(t, result); !errors.Is(got, tc.cause) {
					t.Fatalf("%s terminal error = %v, want originating cause %v", name, got, tc.cause)
				}
			}

			snapshot := shaped.Snapshot()
			if snapshot.AcceptedBytes != snapshot.EmittedBytes+
				snapshot.AsyncWriteErrorBytes+
				snapshot.AsyncWriteEMSGSIZEBytes {
				t.Fatalf("terminal byte conservation failed: %+v", snapshot)
			}
			switch tc.name {
			case "generic":
				if snapshot.AsyncWriteErrors != 1 ||
					snapshot.AsyncWriteErrorBytes != snapshot.AcceptedBytes ||
					snapshot.AsyncWriteEMSGSIZEErrors != 0 ||
					snapshot.AsyncWriteEMSGSIZEBytes != 0 {
					t.Fatalf("generic terminal accounting = %+v", snapshot)
				}
			case "EMSGSIZE":
				if snapshot.AsyncWriteErrors != 0 ||
					snapshot.AsyncWriteErrorBytes != 0 ||
					snapshot.AsyncWriteEMSGSIZEErrors != 1 ||
					snapshot.AsyncWriteEMSGSIZEBytes != snapshot.AcceptedBytes {
					t.Fatalf("EMSGSIZE terminal accounting = %+v", snapshot)
				}
			}
		})
	}
}
