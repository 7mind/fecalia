package shaper

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"syscall"
	"testing"
)

type generatedPriorityOutcome uint8

const (
	generatedPrioritySuccess generatedPriorityOutcome = iota
	generatedPriorityError
	generatedPriorityWrongLength
)

type generatedPriorityCallResult struct {
	admitted   bool
	completion <-chan error
	err        error
	panicValue any
}

func TestGeneratedPriorityRetirementDuringBlockedGenerator(t *testing.T) {
	for _, api := range []string{"blocking", "try"} {
		for _, stop := range []string{"Stop", "StopWithError"} {
			for _, outcome := range []generatedPriorityOutcome{
				generatedPrioritySuccess,
				generatedPriorityError,
				generatedPriorityWrongLength,
			} {
				name := fmt.Sprintf("%s/%s/outcome-%d", api, stop, outcome)
				t.Run(name, func(t *testing.T) {
					const size = 40
					clock := newFakeClock()
					var writerCalls atomic.Int32
					shaped, err := New(validConfig(), clock, func([]byte) error {
						writerCalls.Add(1)
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = shaped.Close() }()

					generatorEntered := make(chan struct{})
					releaseGenerator := make(chan struct{})
					localErr := errors.New("generator failed")
					generate := func() ([]byte, WriteFunc, error) {
						close(generatorEntered)
						<-releaseGenerator
						switch outcome {
						case generatedPrioritySuccess:
							return make([]byte, size), nil, nil
						case generatedPriorityError:
							return nil, nil, localErr
						case generatedPriorityWrongLength:
							return make([]byte, size-1), nil, nil
						default:
							panic("invalid generated-priority outcome")
						}
					}

					result := make(chan generatedPriorityCallResult, 1)
					go func() {
						callResult := generatedPriorityCallResult{}
						defer func() {
							callResult.panicValue = recover()
							if callResult.panicValue != nil && api == "try" {
								// Current-head fail-first cleanup: the panic occurs
								// before TryWritePriorityGenerated balances beginCall.
								shaped.calls.Done()
							}
							result <- callResult
						}()
						switch api {
						case "blocking":
							callResult.admitted = true
							callResult.err = shaped.WritePriorityGenerated(
								context.Background(),
								size,
								generate,
							)
						case "try":
							callResult.admitted, callResult.completion, callResult.err =
								shaped.TryWritePriorityGenerated(size, generate)
						default:
							panic("invalid generated-priority API")
						}
					}()
					<-generatorEntered

					terminalCause := ErrClosed
					if stop == "StopWithError" {
						terminalCause = syscall.EMSGSIZE
						shaped.StopWithError(terminalCause)
					} else {
						shaped.Stop()
					}
					close(releaseGenerator)

					callResult := <-result
					if callResult.panicValue != nil {
						t.Fatalf("generator completion panicked after retirement: %v", callResult.panicValue)
					}
					if !callResult.admitted {
						t.Fatal("accepted generated-priority reservation reported unadmitted")
					}
					switch api {
					case "blocking":
						if !errors.Is(callResult.err, terminalCause) {
							t.Fatalf("blocking completion = %v, want terminal cause %v",
								callResult.err, terminalCause)
						}
					case "try":
						if callResult.err != nil {
							t.Fatalf("try call error = %v, want accepted completion channel", callResult.err)
						}
						if callResult.completion == nil {
							t.Fatal("try call returned no completion for accepted reservation")
						}
						if completionErr := <-callResult.completion; !errors.Is(completionErr, terminalCause) {
							t.Fatalf("try completion = %v, want terminal cause %v",
								completionErr, terminalCause)
						}
					}
					if writerCalls.Load() != 0 {
						t.Fatalf("retired generated priority reached writer %d times", writerCalls.Load())
					}

					snapshot := shaped.Snapshot()
					if snapshot.AcceptedBytes != size ||
						snapshot.EmittedBytes != 0 ||
						snapshot.QueueBytes != 0 ||
						snapshot.PriorityRetainedBytes != 0 ||
						snapshot.InFlightBytes != 0 {
						t.Fatalf("retired generated-priority snapshot = %+v", snapshot)
					}
					if errors.Is(terminalCause, syscall.EMSGSIZE) {
						if snapshot.AsyncWriteEMSGSIZEErrors != 0 ||
							snapshot.AsyncWriteEMSGSIZEBytes != size ||
							snapshot.AsyncWriteErrors != 0 ||
							snapshot.AsyncWriteErrorBytes != 0 {
							t.Fatalf("EMSGSIZE retirement accounting = %+v", snapshot)
						}
					} else if snapshot.AsyncWriteErrors != 0 ||
						snapshot.AsyncWriteErrorBytes != size ||
						snapshot.AsyncWriteEMSGSIZEErrors != 0 ||
						snapshot.AsyncWriteEMSGSIZEBytes != 0 {
						t.Fatalf("generic retirement accounting = %+v", snapshot)
					}

					futureGeneratorCalled := false
					futureErr := shaped.WritePriorityGenerated(
						context.Background(),
						size,
						func() ([]byte, WriteFunc, error) {
							futureGeneratorCalled = true
							return make([]byte, size), nil, nil
						},
					)
					if !errors.Is(futureErr, ErrClosed) || futureGeneratorCalled {
						t.Fatalf("future admission = %v generatorCalled=%v, want ErrClosed before generation",
							futureErr, futureGeneratorCalled)
					}
				})
			}
		}
	}
}
