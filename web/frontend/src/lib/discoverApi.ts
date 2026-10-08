// Only the two node types whose firmware answers a discovery request. simple_repeater and
// simple_sensor implement onControlDataRecv and reply; simple_room_server inherits the empty base
// Mesh::onControlDataRecv, and companion_radio forwards the frame to its phone app without ever
// replying. Offering Companions or Rooms would return an empty list that reads as "none in range".
export const NODE_TYPES = [
  { value: 2, label: "Repeaters" },
  { value: 4, label: "Sensors" },
] as const;

export function nodeTypeLabel(t: number): string {
  switch (t) {
    case 1:
      return "companion";
    case 2:
      return "repeater";
    case 3:
      return "room";
    case 4:
      return "sensor";
    default:
      return `type ${t}`;
  }
}

export interface DiscoveryInfo {
  pubkey: string;
  name: string;
  type: number;
  // snr is how well we heard them; reportedSnr is how well they heard us. Both are real dB.
  snr: number;
  reportedSnr: number;
  // When this node answered. A timestamp, not an age, so the page can tick it without refetching.
  heard: string;
}

export interface DiscoveryState {
  running: boolean;
  secsLeft: number;
  // Empty until a scan has run. Results outlive their scan, so the page must say when they are from.
  scanStartedAt: string;
  results: DiscoveryInfo[];
}

async function call(method: "GET" | "POST", body?: unknown): Promise<DiscoveryState> {
  const res = await fetch("/api/discover", {
    method,
    ...(body ? { headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) } : {}),
  });
  if (!res.ok) {
    const err = (await res.json().catch(() => null)) as { error?: string } | null;
    throw new Error(err?.error || `discovery failed (${res.status})`);
  }
  return (await res.json()) as DiscoveryState;
}

export const discoverApi = {
  state: () => call("GET"),
  start: (types: number[]) => call("POST", { types }),
};

// One repeater in a Discover nearby run. A firmware repeater answers 4 regions requests every 3 minutes from anyone, so "no answer" can be that limit.
export interface RegionScanRepeater {
  pubkey: string;
  name: string;
  snr: number;
  status: "waiting" | "asking" | "answered" | "no answer";
  // As reported: "*" first when it floods unscoped traffic.
  regions: string[];
}

export interface RegionScanState {
  running: boolean;
  phase: "" | "listening" | "asking" | "done";
  secsLeft: number;
  startedAt: string;
  repeaters: RegionScanRepeater[];
}

async function regionScan(method: "GET" | "POST"): Promise<RegionScanState> {
  const res = await fetch("/api/discover/regions", { method });
  if (!res.ok) {
    const err = (await res.json().catch(() => null)) as { error?: string } | null;
    throw new Error(err?.error || `discover nearby failed (${res.status})`);
  }
  return (await res.json()) as RegionScanState;
}

export const regionScanApi = {
  state: () => regionScan("GET"),
  start: () => regionScan("POST"),
};
