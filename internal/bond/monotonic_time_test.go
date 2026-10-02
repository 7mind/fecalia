package bond_test

import (
	"errors"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

func TestTransportRejectsRegressingTime(t *testing.T) {
	for _, operation := range []string{"enqueue", "receive", "path", "poll"} {
		t.Run(operation, func(t *testing.T) {
			now := time.Unix(100, 0)
			p := bond.New(bond.Epoch{Boot: 1, Generation: 1})
			p.SetRemote(bond.Epoch{Boot: 2, Generation: 1}, true)
			p.Path(0, 0, time.Millisecond, now)
			if err := p.Enqueue(make([]byte, 1200), bond.PacketMetadata{}, now); err != nil {
				t.Fatal(err)
			}
			frames, err := p.Poll(now.Add(time.Millisecond))
			if err != nil {
				t.Fatal(err)
			}
			if operation == "path" {
				err = p.Path(1, 1, time.Millisecond, now)
				if err == nil || len(p.Snapshot(now.Add(time.Millisecond)).Paths) != 1 {
					t.Fatal("regressing path update changed the transport")
				}
			} else if operation == "poll" {
				if _, err = p.Poll(now); err == nil {
					t.Fatal("poll accepted regressing time")
				}
			} else if operation == "enqueue" {
				if err := p.Enqueue(make([]byte, 1200), bond.PacketMetadata{}, now); err == nil {
					t.Fatal("enqueue accepted a time before the previous poll")
				}
			} else {
				// The frame is valid for this receiver, isolating the clock violation.
				q := bond.New(bond.Epoch{Boot: 2, Generation: 1})
				q.SetRemote(p.Epoch(), true)
				q.Path(0, 0, time.Millisecond, now)
				q.Poll(now.Add(time.Millisecond))
				if _, err := q.Receive(0, frames[0].Frame, now); err == nil {
					t.Fatal("receive accepted a time before the previous poll")
				}
			}
			if operation == "path" || operation == "poll" {
				var rejected *bond.RejectionError
				if !errors.As(err, &rejected) || rejected.Cause != bond.RejectTime {
					t.Fatalf("expected time rejection, got %v", err)
				}
			}
		})
	}
}
