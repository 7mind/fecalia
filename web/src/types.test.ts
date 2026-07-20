import { describe, expect, it } from 'vitest';
import type { MonitorSnapshot } from './types';

// Captured frames mirroring the JSON monitor.BuildSnapshot actually emits
// (internal/monitor/monitor.go, T214/T218). Kept as literal JSON text (not
// object literals) so parsing these strings genuinely exercises the wire
// format — including PathSnapshot.addressing's `omitempty` (absent on the
// redacted frame, present on the full one) — rather than merely re-stating
// the TypeScript shape.

// Non-loopback binding: BuildSnapshot omits every `addressing` field and
// blanks endpoint addresses server-side (Q62); addressingHidden is true.
const REDACTED_FRAME = `{
  "paths": [
    {
      "name": "wan0",
      "peer": "",
      "txBytes": 1000,
      "rxBytes": 2000,
      "throughputBps": 5000,
      "rttSeconds": 0.02,
      "jitterSeconds": 0.001,
      "loss": 0.01,
      "up": true,
      "bindMode": "device",
      "boundDevice": "eth0",
      "linkBandwidthBps": 1000000,
      "linkRttSeconds": 0.03
    }
  ],
  "fec": [],
  "reseq": [],
  "aggregation": [],
  "session": { "established": true, "lastHandshakeSeconds": 12.5 },
  "peerNames": [],
  "multiPeer": false,
  "daemon": { "role": "edge", "version": "v0.1.0", "uptimeSeconds": 3600 },
  "endpoints": [
    { "address": "", "active": true },
    { "address": "", "active": false }
  ],
  "wgPublicKeyFingerprint": "aGVsbG8gd29",
  "addressingHidden": true
}`;

// Loopback-bound monitor: addressing is revealed, so every `addressing`
// field and endpoint `address` is populated (Q61/Q62); addressingHidden is
// false.
const FULL_FRAME = `{
  "paths": [
    {
      "name": "wan0",
      "peer": "",
      "txBytes": 1000,
      "rxBytes": 2000,
      "throughputBps": 5000,
      "rttSeconds": 0.02,
      "jitterSeconds": 0.001,
      "loss": 0.01,
      "up": true,
      "bindMode": "source",
      "boundDevice": "",
      "linkBandwidthBps": 1000000,
      "linkRttSeconds": 0.03,
      "addressing": { "source": "192.0.2.1", "remote": "198.51.100.7:51820" }
    }
  ],
  "fec": [],
  "reseq": [],
  "aggregation": [],
  "session": { "established": true, "lastHandshakeSeconds": 12.5 },
  "peerNames": [],
  "multiPeer": false,
  "daemon": { "role": "edge", "version": "v0.1.0", "uptimeSeconds": 3600 },
  "endpoints": [
    { "address": "198.51.100.9:51820", "active": true },
    { "address": "198.51.100.10:51820", "active": false }
  ],
  "wgPublicKeyFingerprint": "aGVsbG8gd29",
  "addressingHidden": false
}`;

describe('MonitorSnapshot wire fixtures (T218)', () => {
  it('parses a redacted (non-loopback) frame: addressing is absent, endpoint addresses are blank', () => {
    const snapshot: MonitorSnapshot = JSON.parse(REDACTED_FRAME) as MonitorSnapshot;

    expect(snapshot.addressingHidden).toBe(true);
    expect(snapshot.paths).toHaveLength(1);

    const path = snapshot.paths[0];
    expect(path.bindMode).toBe('device');
    expect(path.boundDevice).toBe('eth0');
    expect(path.linkBandwidthBps).toBe(1000000);
    expect(path.linkRttSeconds).toBe(0.03);

    // Type narrowing: `addressing` is optional, so it must be checked before
    // use. On the redacted frame that check must fail (the field is absent).
    if (path.addressing) {
      throw new Error('redacted frame must not carry an addressing block');
    }
    expect(path.addressing).toBeUndefined();
    expect('addressing' in path).toBe(false);

    expect(snapshot.endpoints).toEqual([
      { address: '', active: true },
      { address: '', active: false },
    ]);

    expect(snapshot.daemon).toEqual({ role: 'edge', version: 'v0.1.0', uptimeSeconds: 3600 });
    expect(snapshot.wgPublicKeyFingerprint).toBe('aGVsbG8gd29');
  });

  it('parses a full (loopback) frame: addressing narrows to a present block on both paths and endpoints', () => {
    const snapshot: MonitorSnapshot = JSON.parse(FULL_FRAME) as MonitorSnapshot;

    expect(snapshot.addressingHidden).toBe(false);
    const path = snapshot.paths[0];

    // Type narrowing: after the guard, `path.addressing` is the non-optional
    // AddressingSnapshot — `.source`/`.remote` are accessible without a cast.
    if (!path.addressing) {
      throw new Error('full frame must carry an addressing block');
    }
    expect(path.addressing.source).toBe('192.0.2.1');
    expect(path.addressing.remote).toBe('198.51.100.7:51820');

    expect(snapshot.endpoints).toEqual([
      { address: '198.51.100.9:51820', active: true },
      { address: '198.51.100.10:51820', active: false },
    ]);
  });

  it('R242/Q63: the wire contract carries the fingerprint ONLY — never a full wgPublicKey field', () => {
    for (const frame of [REDACTED_FRAME, FULL_FRAME]) {
      const parsed = JSON.parse(frame) as Record<string, unknown>;
      expect(parsed.wgPublicKeyFingerprint).toBeTypeOf('string');
      expect('wgPublicKey' in parsed).toBe(false);
    }

    // Compile-time guarantee: MonitorSnapshot has no `wgPublicKey` property to
    // assign into (a phantom `wgPublicKey?` would type-check here but must
    // not exist on the interface).
    // @ts-expect-error -- wgPublicKey is deliberately not part of the contract
    const withPhantomKey: MonitorSnapshot = { ...(JSON.parse(FULL_FRAME) as MonitorSnapshot), wgPublicKey: 'full-key' };
    expect(withPhantomKey).toBeTruthy();
  });
});
