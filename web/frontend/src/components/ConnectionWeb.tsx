import { useMemo, useState } from "react";
import { TriangleAlert, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetTitle,
} from "@/components/ui/sheet";
import { snrTextClass } from "@/components/SignalStrength";
import { peerLatLon } from "@/lib/leaflet";
import { timeAgo } from "@/lib/format";
import { cn } from "@/lib/utils";
import {
  type ConnectionWeb,
  type WebLink,
  type WebNode,
  type WebNeighbour,
  isUncertain,
  km,
  linkKey,
  located,
  nodeName,
  nodeNeighbours,
  pct,
  SELF_ID,
} from "@/lib/connectionWeb";
import { NOT_MEASURED, type ConnectionWebState } from "@/hooks/useConnectionWeb";

// Slider max, as a percent of the busiest link: relative, so it thins a quiet hour and a busy week alike.
const MAX_HIDE_PCT = 25;
// A relay beside us can sit on hundreds of routes; rendering them all at once froze the sheet.
const ROUTES_PAGE = 10;

const PILL =
  "inline-flex items-center gap-1.5 border border-border bg-card px-2 py-1 font-mono text-[10px] uppercase tracking-[0.12em] transition-all hover:border-foreground/40";

// The Connections mode's half of the Map toolbar.
export function ConnectionControls({
  cw,
  ownEchoes,
  onOwnEchoes,
}: {
  cw: ConnectionWebState;
  ownEchoes: boolean;
  onOwnEchoes: (on: boolean) => void;
}) {
  return (
    <>
      <label className="flex items-center gap-2">
        <span className="label-overline">Window</span>
        <Select
          value={String(cw.hours)}
          onValueChange={(v) => cw.setHours(Number(v))}
        >
          <SelectTrigger
            size="sm"
            className="w-28 rounded-none font-mono text-xs"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent className="rounded-none font-mono text-xs">
            {cw.windows.map((w) => (
              <SelectItem
                key={w.hours}
                value={String(w.hours)}
                className="rounded-none font-mono text-xs"
              >
                {w.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </label>
      <label className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="label-overline whitespace-nowrap">Hide under</span>
        <input
          type="range"
          min={0}
          max={MAX_HIDE_PCT}
          step={0.5}
          value={cw.minShare * 100}
          onChange={(e) => cw.setMinShare(Number(e.target.value) / 100)}
          aria-label="Hide links carrying less than this percent of the busiest link"
          className="w-32 accent-primary"
        />
        <span className="w-10 text-right font-mono text-xs tabular-nums">
          {+(cw.minShare * 100).toFixed(1)}%
        </span>
        <span className="label-overline whitespace-nowrap">
          of busiest · {cw.shownLinks}/{cw.totalLinks} links
        </span>
      </label>
      <button
        type="button"
        aria-pressed={ownEchoes}
        onClick={() => onOwnEchoes(!ownEchoes)}
        title="Count your own messages and adverts when other repeaters relay them back to you"
        className={cn(
          PILL,
          ownEchoes ? "border-primary/60 text-primary" : "text-muted-foreground",
        )}
      >
        my packets
      </button>
      {cw.via && (
        <button
          type="button"
          onClick={() => cw.setVia(null)}
          className={cn(PILL, "border-primary/60 text-primary")}
        >
          routes from {nodeName(cw.nodes.get(cw.via), cw.via)}
          <X className="size-3" />
        </button>
      )}
    </>
  );
}

// Below the map: the key, the hops worth checking and the relays the map cannot place.
export function ConnectionLists({ cw }: { cw: ConnectionWebState }) {
  const { uncertain, unplaced } = cw;
  return (
    <>
      <Legend />
      <NodeListDetails
        nodes={uncertain}
        summary={`${uncertain.length} hop${uncertain.length === 1 ? "" : "s"} matching more than one repeater (check these)`}
        name={(n) => `${nodeName(n)} · hash ${n.hash?.toUpperCase()}`}
        detail={(n) => `${n.candidates.length} matches · ${n.observations} pkts`}
        onOpen={cw.openNode}
      />
      <NodeListDetails
        nodes={unplaced}
        summary={`${unplaced.length} relay${unplaced.length === 1 ? "" : "s"} with no position (not drawn)`}
        name={nodeName}
        detail={(n) => `${n.observations} pkts`}
        onOpen={cw.openNode}
      />
    </>
  );
}

// onOpenPeer hands a node over to the Map's own peer sheet.
export function ConnectionSheet({
  cw,
  onOpenPeer,
}: {
  cw: ConnectionWebState;
  onOpenPeer: (pubkey: string) => void;
}) {
  const { selection, web } = cw;
  return (
    <Sheet
      open={selection != null}
      onOpenChange={(open) => !open && cw.setSelection(null)}
    >
      <SheetContent
        side="right"
        className="w-full max-w-[100vw] overflow-y-auto border-l border-border bg-card p-0 sm:max-w-md"
      >
        <SheetTitle className="sr-only">Connection detail</SheetTitle>
        <SheetDescription className="sr-only">
          Statistics for the selected link or node.
        </SheetDescription>
        {selection?.kind === "link" && (
          <LinkDetail
            a={selection.a}
            b={selection.b}
            links={cw.links}
            nodes={cw.nodes}
            posOf={cw.posOf}
            onOpenNode={cw.openNode}
          />
        )}
        {selection?.kind === "node" && web && (
          <NodeDetail
            key={selection.id}
            id={selection.id}
            web={web}
            nodes={cw.nodes}
            self={cw.posOf(SELF_ID)}
            onRepin={cw.repin}
            onOpenNode={cw.openNode}
            onOpenPeer={onOpenPeer}
          />
        )}
      </SheetContent>
    </Sheet>
  );
}

function NodeListDetails({
  nodes,
  summary,
  name,
  detail,
  onOpen,
}: {
  nodes: WebNode[];
  summary: string;
  name: (n: WebNode) => string;
  detail: (n: WebNode) => string;
  onOpen: (id: string) => void;
}) {
  if (nodes.length === 0) return null;
  return (
    <details className="border-t border-border px-4 py-3">
      <summary className="label-overline">{summary}</summary>
      <ul className="mt-2 divide-y divide-border">
        {nodes.map((n) => (
          <li key={n.id}>
            <button
              type="button"
              onClick={() => onOpen(n.id)}
              className="flex w-full items-center justify-between gap-3 py-2 text-left font-mono text-xs hover:text-primary"
            >
              <span className="truncate">{name(n)}</span>
              <span className="tabular-nums text-muted-foreground">
                {detail(n)}
              </span>
            </button>
          </li>
        ))}
      </ul>
    </details>
  );
}

function Legend() {
  const swatch = (color: string, label: string) => (
    <span className="inline-flex items-center gap-1.5">
      <span
        className="inline-block h-0 w-5 border-t-2 border-dashed"
        style={{ borderColor: color }}
        aria-hidden
      />
      {label}
    </span>
  );
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-border px-4 py-3 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
      <span>width = how often a node picks this hop</span>
      <span>brightness = packets carried</span>
      <span>dashes move toward the receiver</span>
      {swatch("var(--signal-strong)", "≥ 0 dB")}
      {swatch("var(--signal-weak)", "≥ −10 dB")}
      {swatch("var(--signal-dead)", "< −10 dB")}
      {swatch(NOT_MEASURED, "no SNR")}
      <span className="inline-flex items-center gap-1.5">
        <span
          className="inline-block size-2.5 rounded-full outline-2 outline-offset-2 outline-dashed outline-foreground"
          aria-hidden
        />
        several matches
      </span>
    </div>
  );
}

function Stat({
  label,
  children,
  className,
}: {
  label: string;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div className="flex items-baseline justify-between gap-3 py-1">
      <span className="label-overline">{label}</span>
      <span className={cn("font-mono text-sm tabular-nums", className)}>
        {children}
      </span>
    </div>
  );
}

function LinkDetail({
  a,
  b,
  links,
  nodes,
  posOf,
  onOpenNode,
}: {
  a: string;
  b: string;
  links: Map<string, WebLink>;
  nodes: Map<string, WebNode>;
  posOf: (id: string) => [number, number] | null;
  onOpenNode: (id: string) => void;
}) {
  const dirs = [links.get(linkKey(a, b)), links.get(linkKey(b, a))].filter(
    (l): l is WebLink => !!l,
  );
  const name = (id: string) => nodeName(nodes.get(id), id);
  return (
    <div className="space-y-5 p-5">
      <div className="space-y-1">
        <h2 className="font-mono text-sm">
          {name(a)} ↔ {name(b)}
        </h2>
        <Stat label="Distance">{km(posOf(a), posOf(b))}</Stat>
        {[a, b]
          .map((id) => nodes.get(id))
          .filter((n): n is WebNode => !!n?.hash)
          .map((n) => (
            <button
              key={n.id}
              type="button"
              onClick={() => onOpenNode(n.id)}
              className={cn(
                "flex w-full items-center gap-2 py-1 text-left font-mono text-xs hover:text-primary",
                isUncertain(n) ? "text-warning" : "text-muted-foreground",
              )}
            >
              {isUncertain(n) && <TriangleAlert className="size-3 shrink-0" />}
              <span className="truncate">
                {nodeName(n)} · hash {n.hash?.toUpperCase()} ·{" "}
                {n.candidates.length} match
                {n.candidates.length === 1 ? "" : "es"}
                {n.pinned ? " · pinned" : ""} · change
              </span>
            </button>
          ))}
      </div>
      {dirs.map((l) => {
        const feeders = [...links.values()]
          .filter((f) => f.to === l.from)
          .sort((x, y) => y.count - x.count);
        return (
          <section
            key={linkKey(l.from, l.to)}
            className="space-y-1 border-t border-border pt-3"
          >
            <h3 className="label-overline text-foreground">
              {name(l.from)} → {name(l.to)}
            </h3>
            <Stat label="Packets">{l.count}</Stat>
            <Stat label={`Share of ${name(l.from)}'s traffic`}>
              {pct(l.shareOut)}
            </Stat>
            <Stat label={`Share of traffic into ${name(l.to)}`}>
              {pct(l.shareIn)}
            </Stat>
            <Stat label="Arrived first">{pct(l.first / l.count)}</Stat>
            <Stat label="Last seen">{timeAgo(l.lastSeen)}</Stat>
            {l.snrN > 0 ? (
              <>
                <Stat
                  label="SNR avg"
                  className={snrTextClass(l.snrSum / l.snrN)}
                >
                  {(l.snrSum / l.snrN).toFixed(1)} dB
                </Stat>
                <Stat label="SNR min / max">
                  {l.snrMin?.toFixed(1)} / {l.snrMax?.toFixed(1)} dB
                </Stat>
                {l.rssiN > 0 && (
                  <Stat label="RSSI avg">
                    {Math.round(l.rssiSum / l.rssiN)} dBm
                  </Stat>
                )}
              </>
            ) : (
              <p className="py-1 font-mono text-xs text-muted-foreground">
                A packet carries the signal of its last hop into you only. This
                link never was that hop.
              </p>
            )}
            {feeders.length > 0 && (
              <div className="pt-2">
                <span className="label-overline">Feeding {name(l.from)}</span>
                <ul className="mt-1 space-y-0.5">
                  {feeders.slice(0, 5).map((f) => (
                    <li
                      key={f.from}
                      className="flex justify-between font-mono text-xs"
                    >
                      <span className="truncate">{name(f.from)}</span>
                      <span className="tabular-nums text-muted-foreground">
                        {f.count} · {pct(f.shareIn)}
                      </span>
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </section>
        );
      })}
    </div>
  );
}

function NodeDetail({
  id,
  web,
  nodes,
  self,
  onRepin,
  onOpenNode,
  onOpenPeer,
}: {
  id: string;
  web: ConnectionWeb;
  nodes: Map<string, WebNode>;
  self: [number, number] | null;
  onRepin: (
    oldId: string,
    hash: string,
    pubkey: string | null | undefined,
  ) => Promise<void>;
  onOpenNode: (id: string) => void;
  onOpenPeer: (pubkey: string) => void;
}) {
  const n = nodes.get(id);
  const { feeding, reaching } = useMemo(
    () => nodeNeighbours(web.chains, id),
    [web, id],
  );
  const name = (nid: string) => nodeName(nodes.get(nid), nid);
  const isSelf = id === SELF_ID;
  const [busy, setBusy] = useState(false);
  const repin = async (pubkey: string | null | undefined) => {
    if (!n?.hash) return;
    setBusy(true);
    await onRepin(id, n.hash, pubkey);
    setBusy(false);
  };
  return (
    <div className="space-y-5 p-5">
      <div>
        <h2 className="font-mono text-sm">{name(id)}</h2>
        <p className="font-mono text-xs text-muted-foreground">
          {isSelf ? (
            "your listener · the last hop of every route"
          ) : (
            <>
              {n?.type ?? "unknown type"}
              {n?.hash && ` · hash ${n.hash.toUpperCase()}`}
            </>
          )}
        </p>
        {n && !n.id.startsWith("h:") && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => onOpenPeer(n.id)}
            className="mt-2 rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
          >
            peer details
          </Button>
        )}
      </div>
      {n?.hash && (
        <section className="space-y-2 border-t border-border pt-3">
          <h3 className="label-overline">
            Which repeater is hash {n.hash.toUpperCase()}?
          </h3>
          <p className="font-mono text-xs text-muted-foreground">
            {identityNote(n)}
          </p>
          <ul className="divide-y divide-border">
            {n.candidates.map((c) => (
              <li
                key={c.id}
                className="flex items-center justify-between gap-3 py-2"
              >
                <div className="min-w-0">
                  <div className="truncate font-mono text-xs">
                    {c.name || c.id.slice(0, 8)}
                  </div>
                  <div className="font-mono text-[10px] text-muted-foreground">
                    {km(self, located(c) ? peerLatLon(c.lat, c.lon) : null)}{" "}
                    from you · heard {timeAgo(c.lastSeen)}
                  </div>
                </div>
                {c.id === n.id ? (
                  <span className="label-overline shrink-0 text-primary">
                    {n.pinned ? "pinned" : "picked"}
                  </span>
                ) : (
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={busy}
                    onClick={() => repin(c.id)}
                    className="shrink-0 rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
                  >
                    use this
                  </Button>
                )}
              </li>
            ))}
          </ul>
          <div className="flex flex-wrap gap-2">
            {n.candidates.length > 0 &&
              !(n.pinned && n.id.startsWith("h:")) && (
                <Button
                  variant="outline"
                  size="sm"
                  disabled={busy}
                  onClick={() => repin(null)}
                  className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
                >
                  none of these
                </Button>
              )}
            {n.pinned && (
              <Button
                variant="ghost"
                size="sm"
                disabled={busy}
                onClick={() => repin(undefined)}
                className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
              >
                back to automatic
              </Button>
            )}
          </div>
        </section>
      )}
      {isSelf && (
        <div className="border-t border-border pt-3">
          <Stat label="Packets received">
            {feeding.reduce((s, h) => s + h.count, 0)}
          </Stat>
          <Stat label="Nodes heard directly">{feeding.length}</Stat>
          {(() => {
            const sum = feeding.reduce((s, h) => s + h.snrSum, 0);
            const n = feeding.reduce((s, h) => s + h.snrN, 0);
            if (n === 0) return null;
            return (
              <Stat label="SNR avg" className={snrTextClass(sum / n)}>
                {(sum / n).toFixed(1)} dB
              </Stat>
            );
          })()}
        </div>
      )}
      {n && (
        <div className="border-t border-border pt-3">
          <Stat label="Packets received">{n.observations}</Stat>
          <Stat label="Different packets">{n.packets}</Stat>
          <Stat label="Times heard per packet">
            {(n.observations / (n.packets || 1)).toFixed(2)}
          </Stat>
          <Stat label="Neighbours in / out">
            {feeding.length} / {reaching.length}
          </Stat>
        </div>
      )}
      <NeighbourList
        title={isSelf ? "Nodes you hear directly" : `Traffic into ${name(id)}`}
        hops={feeding}
        empty="local client traffic"
        label={(hid) => `${name(hid)} →`}
        onOpen={onOpenNode}
      />
      <NeighbourList
        title={`Traffic from ${name(id)} to you`}
        hops={reaching}
        empty="local client traffic"
        label={(hid) => `→ ${name(hid)}`}
        onOpen={onOpenNode}
      />
    </div>
  );
}

// One side of a node's traffic, busiest neighbour first; a row opens that neighbour's sheet.
function NeighbourList({
  title,
  hops,
  empty,
  label,
  onOpen,
}: {
  title: string;
  hops: WebNeighbour[];
  empty: string;
  label: (id: string) => string;
  onOpen: (id: string) => void;
}) {
  const [shown, setShown] = useState(ROUTES_PAGE);
  const hidden = hops.slice(shown);
  if (hops.length === 0) return null;
  return (
    <section className="space-y-2 border-t border-border pt-3">
      <h3 className="label-overline">
        {title} · {hops.length}
      </h3>
      <div className="flex flex-wrap gap-3 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
        <span className="inline-flex items-center gap-1.5">
          <span className="inline-block h-2 w-3 bg-primary" aria-hidden />
          first to arrive
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span className="inline-block h-2 w-3 bg-primary/30" aria-hidden />
          arrived after another path
        </span>
      </div>
      {hops.slice(0, shown).map((hop) => (
        <div key={hop.id ?? "local"} className="space-y-1">
          <div className="flex items-baseline justify-between gap-3">
            {hop.id == null ? (
              <span className="font-mono text-xs text-muted-foreground">
                {empty}
              </span>
            ) : (
              <button
                type="button"
                onClick={() => onOpen(hop.id!)}
                className="min-w-0 truncate text-left font-mono text-xs hover:text-primary"
              >
                {label(hop.id)}
              </button>
            )}
            {hop.snrN > 0 && (
              <span
                className={cn(
                  "shrink-0 font-mono text-[10px] tabular-nums",
                  snrTextClass(hop.snrSum / hop.snrN),
                )}
              >
                {(hop.snrSum / hop.snrN).toFixed(1)} dB
              </span>
            )}
          </div>
          <RouteBar
            share={hop.share}
            firstShare={hop.firstShare}
            count={hop.count}
            first={hop.first}
          />
        </div>
      ))}
      {hidden.length > 0 && (
        <Button
          variant="ghost"
          size="sm"
          onClick={() => setShown((n) => n + ROUTES_PAGE)}
          className="w-full rounded-none font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground"
        >
          show {Math.min(ROUTES_PAGE, hidden.length)} more · {hidden.length}{" "}
          left, {pct(hidden.reduce((sum, hop) => sum + hop.share, 0))} of
          traffic
        </Button>
      )}
    </section>
  );
}

// Solid = arrived first, faded = duplicates after another path; mostly faded = busy but always second.
function RouteBar({
  share,
  firstShare,
  count,
  first,
}: {
  share: number;
  firstShare: number;
  count: number;
  first: number;
}) {
  return (
    <div className="space-y-1">
      <div className="flex h-2 w-full bg-muted">
        <div className="bg-primary" style={{ width: pct(firstShare) }} />
        <div
          className="bg-primary/30"
          style={{ width: pct(share - firstShare) }}
        />
      </div>
      <div className="flex justify-between font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground tabular-nums">
        <span>
          {pct(share)} of traffic · {count} packets
        </span>
        <span>
          {first} first · {count - first} later
        </span>
      </div>
    </div>
  );
}

function identityNote(n: WebNode): string {
  const unknown = n.id.startsWith("h:");
  if (n.pinned && unknown)
    return "You marked this hash as an unknown repeater. The page does not draw its links.";
  if (n.pinned) return "You pinned this hash to this repeater.";
  if (n.candidates.length > 1)
    return `${n.candidates.length} repeaters share this hash. The page picked the one nearest the next hop toward you. If its links are too long to be real, select the correct repeater.`;
  if (n.candidates.length === 1)
    return "Only one known repeater has this hash. If its links are too long to be real, the true relay is a repeater that sent no advert we heard. Select none of these.";
  return "No known repeater has this hash. The page cannot place this relay.";
}
