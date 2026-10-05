import {
  useCallback,
  useDeferredValue,
  useEffect,
  useMemo,
  useRef,
  useState,
  type RefObject,
} from "react";
import L from "leaflet";
import { toast } from "sonner";
import { useApiObject } from "@/hooks/useApiObject";
import { useResume } from "@/lib/resume";
import { PEER_TYPE_HEX } from "@/components/StatusIndicator";
import { snrFill } from "@/components/SignalStrength";
import { originIcon } from "@/components/DiscoverMap";
import { peerLatLon, wrapLon } from "@/lib/leaflet";
import {
  type ConnectionWeb,
  type WebNode,
  foldLinks,
  isUncertain,
  linkKey,
  located,
  nodeName,
  pinHop,
  SELF_ID,
  unpinHop,
} from "@/lib/connectionWeb";

const WINDOWS = [
  { hours: 1, label: "1h" },
  { hours: 6, label: "6h" },
  { hours: 24, label: "24h" },
  { hours: 72, label: "3d" },
  { hours: 168, label: "7d" },
  { hours: 720, label: "30d" },
];
// The page reads the packet log, not the radio, so following live traffic costs no airtime.
const REFETCH_MS = 60_000;
export const NOT_MEASURED = "var(--muted-foreground)";
// Dash then gap; the flow animation in index.css shifts by exactly this period.
const LINK_DASH = "10 8";

export type Selection =
  { kind: "link"; a: string; b: string } | { kind: "node"; id: string } | null;

function dotIcon(color: string, ambiguous: boolean): L.DivIcon {
  const ring = ambiguous
    ? "outline:2px dashed var(--foreground);outline-offset:3px;"
    : "";
  return L.divIcon({
    className: "meshcore-web-dot",
    html: `<span style="display:grid;place-items:center;width:24px;height:24px;"><span style="display:block;width:12px;height:12px;border-radius:9999px;background:${color};box-shadow:0 0 0 2px rgba(0,0,0,0.55);${ring}"></span></span>`,
    iconSize: [24, 24],
    iconAnchor: [12, 12],
  });
}

const ORIGIN_ICON = originIcon();

// The Map page's Connections mode: routes toward us from the packet log, drawn on the page's map while active.
export function useConnectionWeb(
  active: boolean,
  ownEchoes: boolean,
  mapRef: RefObject<L.Map | null>,
) {
  const [hours, setHours] = useState(24);
  // Starts strict: unfiltered, a busy mesh is a tangle of one-off links.
  const [minShare, setMinShare] = useState(0.1);
  const [via, setVia] = useState<string | null>(null);
  const [selection, setSelection] = useState<Selection>(null);

  const { item: web, loading, error, reload } = useApiObject<ConnectionWeb>(
    active ? `/api/connection-web?hours=${hours}&ownEchoes=${ownEchoes}` : null,
    "Failed to load the connections",
  );
  useResume(reload);

  // Called for each WS packet: one refetch a minute at most, however busy the mesh.
  const pending = useRef<number | null>(null);
  const onPacket = useCallback(() => {
    if (pending.current != null) return;
    pending.current = window.setTimeout(() => {
      pending.current = null;
      reload();
    }, REFETCH_MS);
  }, [reload]);
  useEffect(() => () => window.clearTimeout(pending.current ?? undefined), []);

  const nodes = useMemo(() => {
    const m = new Map<string, WebNode>();
    for (const n of web?.nodes ?? []) m.set(n.id, n);
    return m;
  }, [web]);
  const chains = useMemo(() => web?.chains ?? [], [web]);
  const links = useMemo(
    () => foldLinks(chains, via ?? undefined),
    [chains, via],
  );

  // One line per node pair, dashes flowing in the busier direction.
  const allPairs = useMemo(() => {
    const out = new Map<
      string,
      {
        a: string;
        b: string;
        count: number;
        heavier: number;
        share: number;
        snr: number | null;
      }
    >();
    for (const l of links.values()) {
      const key = linkKey(...([l.from, l.to].sort() as [string, string]));
      const p = out.get(key) ?? {
        a: l.from,
        b: l.to,
        count: 0,
        heavier: 0,
        share: 0,
        snr: null,
      };
      p.count += l.count;
      p.share = Math.max(p.share, l.shareOut);
      if (l.count > p.heavier) {
        p.heavier = l.count;
        p.a = l.from;
        p.b = l.to;
      }
      if (l.snrN > 0) p.snr = l.snrSum / l.snrN;
      out.set(key, p);
    }
    return [...out.values()];
  }, [links]);

  // Deferred so dragging the slider stays smooth while a large mesh redraws behind it.
  const filterShare = useDeferredValue(minShare);
  const pairs = useMemo(() => {
    const busiest = Math.max(...allPairs.map((p) => p.count), 0);
    return allPairs.filter((p) => p.count >= filterShare * busiest);
  }, [allPairs, filterShare]);

  const shownNodeIds = useMemo(() => {
    const s = new Set<string>();
    for (const p of pairs) {
      s.add(p.a);
      s.add(p.b);
    }
    s.delete(SELF_ID);
    return s;
  }, [pairs]);

  const { unplaced, uncertain } = useMemo(() => {
    const shown = [...shownNodeIds]
      .map((id) => nodes.get(id))
      .filter((n): n is WebNode => !!n)
      .sort((a, b) => b.observations - a.observations);
    return {
      unplaced: shown.filter((n) => !located(n)),
      uncertain: shown.filter(isUncertain),
    };
  }, [shownNodeIds, nodes]);

  const openNode = useCallback((id: string) => {
    // Every route ends at us, so filtering the map by "you" would hide nothing.
    setVia(id === SELF_ID ? null : id);
    setSelection({ kind: "node", id });
  }, []);

  // pubkey: a candidate, null = "none of these", undefined = automatic; the sheet and filter follow the new id.
  const repin = useCallback(
    async (oldId: string, hash: string, pubkey: string | null | undefined) => {
      try {
        if (pubkey === undefined) await unpinHop(hash);
        else await pinHop(hash, pubkey);
      } catch (e) {
        toast.error(
          `Could not change the match: ${e instanceof Error ? e.message : "error"}`,
        );
        return;
      }
      const newId = pubkey === undefined ? null : (pubkey ?? `h:${hash}`);
      setVia((v) => (v === oldId ? newId : v));
      setSelection(newId ? { kind: "node", id: newId } : null);
      reload();
    },
    [reload],
  );

  const totalObservations = useMemo(
    () => chains.reduce((s, c) => s + c.count, 0),
    [chains],
  );

  const posOf = useCallback(
    (id: string): [number, number] | null => {
      // Wrapped like every peer: a configured -185 is 175E, and unwrapped it lands a world copy away.
      if (id === SELF_ID)
        return web?.self ? [web.self.lat, wrapLon(web.self.lon)] : null;
      const n = nodes.get(id);
      return located(n) ? peerLatLon(n!.lat, n!.lon) : null;
    },
    [nodes, web],
  );

  const layerRef = useRef<L.LayerGroup | null>(null);
  const fittedRef = useRef(false);
  const fittedVia = useRef(via);

  // The layer lives only while the mode is on; leaving drops it and the open sheet, and coming back fits again.
  useEffect(() => {
    const map = mapRef.current;
    if (!active || !map) return;
    const layer = L.layerGroup().addTo(map);
    layerRef.current = layer;
    return () => {
      layer.remove();
      layerRef.current = null;
      fittedRef.current = false;
      setSelection(null);
    };
  }, [active, mapRef]);

  // Full clear-and-redraw: a refetch replaces every number, so there is nothing to diff against.
  useEffect(() => {
    const map = mapRef.current;
    const layer = layerRef.current;
    if (!map || !layer) return;
    layer.clearLayers();

    // Log-scale brightness: one busy link can out-count the rest of the mesh combined.
    const busiest = Math.max(...pairs.map((p) => p.count), 1);
    const drawn = [...pairs].sort((x, y) => x.share - y.share);
    for (const p of drawn) {
      const a = posOf(p.a);
      const b = posOf(p.b);
      if (!a || !b) continue;
      const weight = 1.5 + 5 * p.share;
      L.polyline([a, b], {
        color: p.snr != null ? snrFill(p.snr) : NOT_MEASURED,
        weight,
        opacity: 0.25 + 0.65 * (Math.log(p.count + 1) / Math.log(busiest + 1)),
        dashArray: LINK_DASH,
        // Round caps would swell each dash into a bead on a wide line.
        lineCap: "butt",
        className: "meshcore-web-link",
        interactive: false,
      }).addTo(layer);
      // An invisible wide twin is the tap target: the drawn line can be 2px on a phone.
      L.polyline([a, b], {
        color: "#000",
        opacity: 0,
        weight: Math.max(16, weight + 12),
      })
        .on("click", () => setSelection({ kind: "link", a: p.a, b: p.b }))
        .addTo(layer);
    }

    const points: [number, number][] = [];
    for (const id of shownNodeIds) {
      const n = nodes.get(id);
      const at = posOf(id);
      if (!n || !at) continue;
      points.push(at);
      L.marker(at, {
        icon: dotIcon(
          PEER_TYPE_HEX[n.type ?? ""] ?? PEER_TYPE_HEX.NONE,
          isUncertain(n),
        ),
      })
        .bindTooltip(nodeName(n))
        .on("click", () => openNode(id))
        .addTo(layer);
    }
    const self = posOf(SELF_ID);
    if (self) {
      points.push(self);
      L.marker(self, { icon: ORIGIN_ICON, zIndexOffset: 1000 })
        .bindTooltip("You")
        .on("click", () => openNode(SELF_ID))
        .addTo(layer);
    }

    // Picking or clearing "routes from" frames the routes now shown.
    if (fittedVia.current !== via) {
      fittedVia.current = via;
      fittedRef.current = false;
    }
    if (!fittedRef.current && points.length > 0) {
      map.fitBounds(L.latLngBounds(points), { padding: [40, 40], maxZoom: via ? 15 : 12 });
      fittedRef.current = true;
    }
  }, [active, mapRef, pairs, shownNodeIds, nodes, posOf, openNode, via]);

  const windows = useMemo(() => {
    const max = (web?.retentionDays ?? 7) * 24;
    const opts = WINDOWS.filter((w) => w.hours < max);
    return [...opts, { hours: max, label: `all (${max / 24}d)` }];
  }, [web?.retentionDays]);

  return {
    web,
    loading,
    error,
    reload,
    onPacket,
    hours,
    setHours,
    windows,
    minShare,
    setMinShare,
    shownLinks: pairs.length,
    totalLinks: allPairs.length,
    via,
    setVia,
    nodes,
    links,
    chains,
    unplaced,
    uncertain,
    totalObservations,
    selection,
    setSelection,
    openNode,
    repin,
    posOf,
  };
}

export type ConnectionWebState = ReturnType<typeof useConnectionWeb>;
