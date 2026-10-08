import { useCallback, useState } from "react";
import { RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { cn } from "@/lib/utils";
import { apiErrorMessage } from "@/lib/apiError";
import { formatDateTime } from "@/lib/format";
import { useRemoteFetch } from "@/lib/remoteFetch";

interface SeriesEntry {
  channel: number;
  type: number;
  name: string;
  unit?: string;
  min: number;
  max: number;
  avg: number;
}

interface Series {
  nodeTime: number;
  entries: SeriesEntry[];
  raw: string;
}

const WINDOWS: { label: string; secs: number }[] = [
  { label: "1h", secs: 3600 },
  { label: "6h", secs: 6 * 3600 },
  { label: "24h", secs: 86400 },
  { label: "7d", secs: 7 * 86400 },
];

// One radio round trip per request (firmware GET_AVG_MIN_MAX), so nothing fires until the operator asks.
export function SeriesPanel({ apiBase }: { apiBase: string }) {
  const [win, setWin] = useState(WINDOWS[2]);
  const [data, setData] = useState<Series | null>(null);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const remoteFetch = useRemoteFetch();

  const request = useCallback(
    async (w = win) => {
      setLoading(true);
      setErr(null);
      try {
        const r = await remoteFetch(`${apiBase}/history?from=${w.secs}&to=0`);
        if (!r.ok) throw new Error(await apiErrorMessage(r));
        setData((await r.json()) as Series);
      } catch (e) {
        setErr(e instanceof Error ? e.message : "Failed");
      } finally {
        setLoading(false);
      }
    },
    [apiBase, win, remoteFetch],
  );

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="label-overline">history · min / avg / max</span>
        <div className="flex items-center gap-2">
          <div className="flex items-center gap-px border border-border bg-border">
            {WINDOWS.map((w) => (
              <button
                key={w.label}
                type="button"
                disabled={loading}
                onClick={() => {
                  setWin(w);
                  if (data) request(w);
                }}
                className={cn(
                  "relative px-2.5 py-1 bg-card font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground",
                  "before:absolute before:inset-x-0 before:-inset-y-2 before:content-[''] sm:before:hidden",
                  "disabled:opacity-50 disabled:pointer-events-none",
                  w.label === win.label && "bg-primary/10 text-primary",
                )}
              >
                {w.label}
              </button>
            ))}
          </div>
          <Button
            variant="outline"
            size="sm"
            onClick={() => request()}
            disabled={loading}
            className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
          >
            <RefreshCw className={cn("size-3", loading && "animate-spin")} />
            {data ? "refresh" : "request"}
          </Button>
        </div>
      </div>
      {err && (
        <Alert variant="destructive">
          <AlertTitle className="font-mono uppercase tracking-widest">Error</AlertTitle>
          <AlertDescription>{err}</AlertDescription>
        </Alert>
      )}
      {data && data.entries.length === 0 && (
        <div className="panel py-10 text-center font-mono text-xs uppercase tracking-[0.12em] text-muted-foreground/60">
          no readings in the last {win.label}
        </div>
      )}
      {data && data.entries.length > 0 && (
        <div className="space-y-2">
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-px bg-border border border-border">
            {data.entries.map((e) => (
              <SeriesTile key={`${e.channel}-${e.type}`} entry={e} />
            ))}
          </div>
          <p className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/60 tabular-nums">
            sensor clock {formatDateTime(new Date(data.nodeTime * 1000).toISOString())}
          </p>
        </div>
      )}
    </div>
  );
}

function SeriesTile({ entry }: { entry: SeriesEntry }) {
  const fmt = (v: number) => {
    const abs = Math.abs(v);
    return v.toFixed(abs >= 100 ? 0 : abs >= 10 ? 1 : 2);
  };
  const span = entry.max - entry.min;
  const pct = span > 0 ? ((entry.avg - entry.min) / span) * 100 : 50;
  return (
    <div className="bg-card p-4 space-y-3">
      <div className="flex items-baseline justify-between gap-2">
        <span className="label-overline truncate">{entry.name}</span>
        <span className="font-mono text-[10px] text-muted-foreground/60 tabular-nums shrink-0">
          ch{entry.channel}
        </span>
      </div>
      <div className="font-mono text-2xl font-semibold tabular-nums">
        {fmt(entry.avg)}
        {entry.unit && (
          <span className="ml-1 text-xs font-normal text-muted-foreground">{entry.unit}</span>
        )}
      </div>
      <div className="space-y-1">
        <div className="relative h-1 bg-muted">
          <div className="absolute inset-y-0 left-0 right-0 bg-primary/30" />
          <div
            className="absolute -top-0.5 size-2 rounded-sm bg-primary"
            style={{ left: `calc(${pct}% - 4px)` }}
          />
        </div>
        <div className="flex justify-between font-mono text-[10px] text-muted-foreground tabular-nums">
          <span>min {fmt(entry.min)}</span>
          <span>max {fmt(entry.max)}</span>
        </div>
      </div>
    </div>
  );
}
