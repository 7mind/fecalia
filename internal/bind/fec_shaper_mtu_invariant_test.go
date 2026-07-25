package bind

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
)

func loadFECShaperInvariantConfig(t testing.TB, kdata, mmax int) config.PathShaperConfig {
	t.Helper()
	encodeKey := func(seed byte) string {
		key := testKey(t, seed).Bytes()
		return base64.StdEncoding.EncodeToString(key[:])
	}
	body := fmt.Sprintf(`
role = "concentrator"
psk = %q

[[paths]]
name = "wan"
source_addr = "192.0.2.10"
link_bandwidth = "80Mbit"
link_rtt = "1ms"

[wireguard]
private_key = %q
listen_port = 51820

[[wireguard.peers]]
public_key = %q
allowed_ips = ["10.0.0.2/32"]

[scheduler]
policy = "active-backup"
pacing_enabled = true

[fec]
enabled = true
data_shards = %d
parity_shards = %d
`, encodeKey(0x81), encodeKey(0x82), encodeKey(0x83), kdata, mmax)
	path := filepath.Join(t.TempDir(), "wanbond.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Scheduler.PerPathShapers) != 1 {
		t.Fatalf("derived path shapers = %d, want 1", len(loaded.Scheduler.PerPathShapers))
	}
	return loaded.Scheduler.PerPathShapers[0]
}

func TestFECShaperOwnershipMatchesProductionFECMTUAndWire(t *testing.T) {
	const (
		kdata = 3
		mmax  = 1
	)
	derived := loadFECShaperInvariantConfig(t, kdata, mmax)
	maxInnerDatagram := InnerMTU(DefaultPathMTU, true) + WGTransportOverhead

	encoder, err := fec.NewEncoder(fec.Config{
		DataShards:   kdata,
		ParityShards: mmax,
		Deadline:     time.Second,
	}, newFakeClock())
	if err != nil {
		t.Fatal(err)
	}
	codec, err := frame.NewCodec(testKey(t, 0x84))
	if err != nil {
		t.Fatal(err)
	}

	var data []fec.DataShard
	var parity []fec.ParityShard
	var dataWires [][]byte
	for index := range kdata {
		payload := fecShardPayload(uint64(index+1), make([]byte, maxInnerDatagram))
		shard, produced, admitErr := encoder.Admit(payload)
		if admitErr != nil {
			t.Fatal(admitErr)
		}
		data = append(data, shard)
		wire, encodeErr := codec.Encode(nil, frame.Data{
			OuterSeq: uint64(index + 1),
			FECGroup: uint32(shard.Group),
			FECIndex: uint8(shard.Index),
			Payload:  payload[fecSeqPrefixLen:],
		})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		dataWires = append(dataWires, wire)
		parity = append(parity, produced...)
	}
	if len(parity) != mmax {
		t.Fatalf("parity shards = %d, want %d", len(parity), mmax)
	}
	var parityWires [][]byte
	for _, shard := range parity {
		wire, encodeErr := codec.Encode(nil, frame.Parity{
			FECGroup:    uint32(shard.Group),
			ParityIndex: uint16(shard.Index),
			DataCount:   uint8(shard.DataCount),
			Payload:     shard.Payload,
		})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		parityWires = append(parityWires, wire)
	}
	if got := len(dataWires[0]); got != derived.MaxEncodedDatagramBytes-FECParityMTUPenalty {
		t.Fatalf("maximum FEC DATA wire = %d, want Lmax-penalty=%d",
			got, derived.MaxEncodedDatagramBytes-FECParityMTUPenalty)
	}
	if got := len(parityWires[0]); got != derived.MaxEncodedDatagramBytes {
		t.Fatalf("maximum FEC PARITY wire = %d, want Lmax=%d", got, derived.MaxEncodedDatagramBytes)
	}

	codedInputOwnership := 0
	for _, shard := range data {
		codedInputOwnership += len(shard.Payload)
	}
	workspaceOwnership := (len(data) + len(parity)) * len(parity[0].Payload)
	encodedWireOwnership := 0
	for _, wire := range dataWires {
		encodedWireOwnership += len(wire)
	}
	for _, wire := range parityWires {
		encodedWireOwnership += len(wire)
	}
	wantFgroup := codedInputOwnership + workspaceOwnership + encodedWireOwnership
	if derived.FECGroupReserveBytes != wantFgroup {
		t.Fatalf("Fgroup = %d, want production ownership %d (coded=%d workspace=%d encoded=%d)",
			derived.FECGroupReserveBytes,
			wantFgroup,
			codedInputOwnership,
			workspaceOwnership,
			encodedWireOwnership,
		)
	}
	wantMtotal := derived.DataBurstBytes +
		derived.ControlReserveBytes +
		derived.PriorityReserveBytes +
		wantFgroup +
		derived.MaxEncodedDatagramBytes
	if derived.MemoryBoundBytes != wantMtotal {
		t.Fatalf("Mtotal = %d, want exact B+C+P+Fgroup+Lio=%d",
			derived.MemoryBoundBytes, wantMtotal)
	}
}
