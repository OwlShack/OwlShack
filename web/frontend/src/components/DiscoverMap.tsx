import { useEffect, useRef, useState } from "react";
import L from "leaflet";
import { themeTileLayer, useThemeTiles } from "@/lib/leaflet";
import { snrFill } from "@/components/SignalStrength";
import { drawLink, LINK_STAGGER } from "@/lib/mapLinks";
import { cn } from "@/lib/utils";

export interface DiscoverMapNode {
  pubkey: string;
  name: string;
  snr: number;
  reportedSnr: number;
  lat: number;
  lon: number;
}

// Sized to stay legible against a tile background, and coloured by how well we hear the node so the
// map answers the same question the table does.
function dotIcon(color: string): L.DivIcon {
  return L.divIcon({
    className: "meshcore-discover-dot",
    html: `<span style="display:block;width:14px;height:14px;border-radius:9999px;background:${color};box-shadow:0 0 0 2px rgba(0,0,0,0.55),0 0 8px ${color};"></span>`,
    iconSize: [14, 14],
    iconAnchor: [7, 7],
  });
}

export function originIcon(): L.DivIcon {
  return L.divIcon({
    className: "meshcore-discover-origin",
    html: `<span style="display:block;width:16px;height:16px;border-radius:9999px;border:3px solid var(--primary);background:transparent;box-shadow:0 0 0 2px rgba(0,0,0,0.65),0 0 8px var(--primary);"></span>`,
    iconSize: [16, 16],
    iconAnchor: [8, 8],
  });
}

export function DiscoverMap({
  nodes,
  origin,
  onSelect,
  className,
}: {
  nodes: DiscoverMapNode[];
  // Our own position, null when this node has no coordinates configured: every link starts here, so
  // without it there is nothing to draw a line from.
  origin: [number, number] | null;
  onSelect: (pubkey: string) => void;
  className?: string;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<L.Map | null>(null);
  const tileRef = useRef<L.TileLayer | null>(null);
  const layerRef = useRef<L.LayerGroup | null>(null);
  const linksRef = useRef<L.LayerGroup | null>(null);
  // Bumped on zoom so the links redraw: whether a label fits depends on the current scale.
  const [zoomTick, setZoomTick] = useState(0);

  useEffect(() => {
    if (!containerRef.current || mapRef.current) return;
    const map = L.map(containerRef.current, {
      zoomControl: true,
      attributionControl: true,
    }).setView([0, 0], 2);
    tileRef.current = themeTileLayer().addTo(map);
    layerRef.current = L.layerGroup().addTo(map);
    linksRef.current = L.layerGroup().addTo(map);
    map.on("zoomend", () => setZoomTick((t) => t + 1));
    mapRef.current = map;
    return () => {
      map.remove();
      mapRef.current = null;
      tileRef.current = null;
      layerRef.current = null;
      linksRef.current = null;
    };
  }, []);

  useThemeTiles(mapRef, tileRef);

  useEffect(() => {
    const map = mapRef.current;
    const layer = layerRef.current;
    if (!map || !layer) return;
    layer.clearLayers();
    if (nodes.length === 0) return;

    nodes.forEach((n) => {
      L.marker([n.lat, n.lon], { icon: dotIcon(snrFill(n.snr)) })
        .addTo(layer)
        .bindTooltip(
          `${n.name || "unknown"} · we hear ${n.snr.toFixed(2)} dB · they hear ${n.reportedSnr.toFixed(2)} dB`,
        )
        .on("click", () => onSelect(n.pubkey));
    });

    // Last and lifted: a node can sit metres from us, and our own position is the one marker that
    // must never end up hidden under another.
    if (origin) {
      L.marker(origin, { icon: originIcon(), zIndexOffset: 1000 })
        .addTo(layer)
        .bindTooltip("this node");
    }

    // Include ourselves in the fit, or the links run off the edge of the view.
    const pts = nodes.map((n) => [n.lat, n.lon] as [number, number]);
    if (origin) pts.push(origin);
    if (pts.length === 1) {
      map.setView(pts[0], 13);
    } else {
      map.fitBounds(L.latLngBounds(pts), { padding: [32, 32], maxZoom: 15 });
    }
  }, [nodes, origin, onSelect]);

  useEffect(() => {
    const map = mapRef.current;
    const links = linksRef.current;
    if (!map || !links) return;
    links.clearLayers();
    if (!origin) return;
    nodes.forEach((n, i) => {
      // A to B is us to them, which is the SNR they reported for our request.
      drawLink(
        map,
        links,
        origin,
        [n.lat, n.lon],
        n.reportedSnr,
        n.snr,
        LINK_STAGGER[i % LINK_STAGGER.length],
      );
    });
  }, [nodes, origin, zoomTick]);

  // Leaflet measures the container on creation, so a tab that mounts hidden comes back zero-height.
  useEffect(() => {
    const map = mapRef.current;
    if (!map) return;
    const obs = new ResizeObserver(() => map.invalidateSize());
    if (containerRef.current) obs.observe(containerRef.current);
    return () => obs.disconnect();
  }, []);

  return (
    <div
      className={cn(
        "relative h-[300px] sm:h-[460px] border border-border rounded-md overflow-hidden",
        className,
      )}
    >
      <div ref={containerRef} className="absolute inset-0" />
    </div>
  );
}
