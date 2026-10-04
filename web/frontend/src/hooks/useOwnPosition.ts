import { useEffect, useState } from "react";
import type { ConfigCompanion, ConfigRepeater } from "@/lib/configApi";
import { wrapLon } from "@/lib/leaflet";

// Our own position in decimal degrees, or null when neither node has one configured. Both
// personalities share one radio, so either coordinate describes the same antenna; the repeater is
// preferred only to match the node discovery sends from.
export function useOwnPosition(): [number, number] | null {
  const [pos, setPos] = useState<[number, number] | null>(null);

  useEffect(() => {
    let live = true;
    const at = (lat: number | null, lon: number | null): [number, number] | null =>
      // Config does not range-check longitude; wrapped so a -185 lands on the same world copy as the peers.
      lat != null && lon != null && (lat !== 0 || lon !== 0) ? [lat, wrapLon(lon)] : null;

    Promise.all([
      fetch("/api/config/repeater").then((r) => (r.ok ? r.json() : null)),
      fetch("/api/config/companions").then((r) => (r.ok ? r.json() : null)),
    ])
      .then(([rep, comps]: [ConfigRepeater | null, ConfigCompanion[] | null]) => {
        if (!live) return;
        const fromRep = rep ? at(rep.latitude, rep.longitude) : null;
        const fromComp = (comps ?? []).map((c) => at(c.latitude, c.longitude)).find(Boolean);
        setPos(fromRep ?? fromComp ?? null);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, []);

  return pos;
}
