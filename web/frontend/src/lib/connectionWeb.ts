// Folds the routes /api/connection-web returns into links and per-node route breakdowns.
import { request } from "@/lib/configApi";

export const SELF_ID = "self";

// A repeater whose key starts with a hop's hash, so it could be the node that relayed it.
export interface WebCandidate {
  id: string;
  name: string;
  lat: number;
  lon: number;
  lastSeen: string;
}

export interface WebNode {
  id: string;
  name?: string;
  type?: string;
  lat: number; // degrees x 1e6; 0,0 = no position
  lon: number;
  hash?: string;
  candidates: WebCandidate[];
  // The operator chose this hash's owner; the distance pick no longer applies.
  pinned: boolean;
  observations: number;
  packets: number;
}

// One distinct route, source side first and ending at "self"; the SNR/RSSI belong to its last link.
export interface WebChain {
  nodes: string[];
  count: number;
  first: number;
  lastSeen: string;
  snrSum: number;
  snrN: number;
  snrMin: number | null;
  snrMax: number | null;
  rssiSum: number;
  rssiN: number;
}

export interface ConnectionWeb {
  self: { lat: number; lon: number } | null;
  hours: number;
  retentionDays: number;
  nodes: WebNode[];
  chains: WebChain[];
}

export interface WebLink {
  from: string;
  to: string;
  count: number;
  first: number;
  lastSeen: string;
  // Of the traffic `from` passed on toward us, the part that went to `to` next.
  shareOut: number;
  // Of everything reaching `to` from a known node, the part that came over this link.
  shareIn: number;
  snrSum: number;
  snrN: number;
  snrMin: number | null;
  snrMax: number | null;
  rssiSum: number;
  rssiN: number;
}

// A node's traffic with one neighbour, one direction; a null feeding id is traffic heard straight from a client.
export interface WebNeighbour {
  id: string | null;
  count: number;
  first: number;
  // Of the copies heard through this node, the part that took this route; duplicates count.
  share: number;
  // The part that beat every other path to us; same denominator as share, so the two stack into one bar.
  firstShare: number;
  // Only the hop into us has a measured signal, so these stay 0 on every link further out.
  snrSum: number;
  snrN: number;
}

// A hop is uncertain while several repeaters share its hash and nobody has said which one it is.
export const isUncertain = (n: WebNode) => n.candidates.length > 1 && !n.pinned;

// pinHop says which peer owns a hash; null says none of the known repeaters does.
export const pinHop = (hash: string, pubkey: string | null) =>
  request(`/api/connection-web/pins/${hash}`, "PUT", { pubkey });

export const unpinHop = (hash: string) =>
  request(`/api/connection-web/pins/${hash}`, "DELETE");

export const linkKey = (from: string, to: string) => `${from}>${to}`;

export function foldLinks(
  chains: WebChain[],
  via?: string,
): Map<string, WebLink> {
  const links = new Map<string, WebLink>();
  const into = new Map<string, number>();
  const outOf = new Map<string, number>();
  for (const c of chains) {
    // Kept whole, not trimmed to after `via`: the hops feeding a relay are half of what it carries.
    if (via && !c.nodes.includes(via)) continue;
    const nodes = c.nodes;
    for (let i = 0; i + 1 < nodes.length; i++) {
      const from = nodes[i];
      const to = nodes[i + 1];
      const key = linkKey(from, to);
      let l = links.get(key);
      if (!l) {
        l = {
          from,
          to,
          count: 0,
          first: 0,
          lastSeen: "",
          shareOut: 0,
          shareIn: 0,
          snrSum: 0,
          snrN: 0,
          snrMin: null,
          snrMax: null,
          rssiSum: 0,
          rssiN: 0,
        };
        links.set(key, l);
      }
      l.count += c.count;
      l.first += c.first;
      if (c.lastSeen > l.lastSeen) l.lastSeen = c.lastSeen;
      into.set(to, (into.get(to) ?? 0) + c.count);
      outOf.set(from, (outOf.get(from) ?? 0) + c.count);
      if (i + 2 === nodes.length) {
        l.snrSum += c.snrSum;
        l.snrN += c.snrN;
        if (c.snrMin != null && (l.snrMin == null || c.snrMin < l.snrMin))
          l.snrMin = c.snrMin;
        if (c.snrMax != null && (l.snrMax == null || c.snrMax > l.snrMax))
          l.snrMax = c.snrMax;
        l.rssiSum += c.rssiSum;
        l.rssiN += c.rssiN;
      }
    }
  }
  for (const l of links.values()) {
    l.shareOut = l.count / (outOf.get(l.from) || 1);
    l.shareIn = l.count / (into.get(l.to) || 1);
  }
  return links;
}

// nodeNeighbours splits a node's traffic by adjacent hop; whole half-routes ran to thousands of rows near us.
export function nodeNeighbours(
  chains: WebChain[],
  nodeId: string,
): { feeding: WebNeighbour[]; reaching: WebNeighbour[] } {
  const feeding = new Map<string | null, WebNeighbour>();
  const reaching = new Map<string | null, WebNeighbour>();
  let total = 0;
  const add = (
    into: Map<string | null, WebNeighbour>,
    id: string | null,
    c: WebChain,
    // The chain's SNR belongs to its last link, so only that link may take it.
    last: boolean,
  ) => {
    let hop = into.get(id);
    if (!hop) {
      hop = {
        id,
        count: 0,
        first: 0,
        share: 0,
        firstShare: 0,
        snrSum: 0,
        snrN: 0,
      };
      into.set(id, hop);
    }
    hop.count += c.count;
    hop.first += c.first;
    if (last) {
      hop.snrSum += c.snrSum;
      hop.snrN += c.snrN;
    }
  };
  for (const c of chains) {
    const at = c.nodes.indexOf(nodeId);
    if (at < 0) continue;
    const end = c.nodes.length - 1;
    // A null feeder is a packet this node heard from a client: a path names repeaters only.
    add(feeding, at > 0 ? c.nodes[at - 1] : null, c, at === end);
    if (at < end) add(reaching, c.nodes[at + 1], c, at + 1 === end);
    total += c.count;
  }
  const finish = (m: Map<string | null, WebNeighbour>) => {
    const out = [...m.values()];
    for (const hop of out) {
      hop.share = hop.count / (total || 1);
      hop.firstShare = hop.first / (total || 1);
    }
    return out.sort((a, b) => b.count - a.count);
  };
  return { feeding: finish(feeding), reaching: finish(reaching) };
}
