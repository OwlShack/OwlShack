import { useCallback, useEffect, useRef, useState } from "react";
import { RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Skeleton } from "@/components/ui/skeleton";
import { CoordLink } from "@/components/CoordLink";
import { cn } from "@/lib/utils";
import { apiErrorMessage } from "@/lib/apiError";
import { useRemoteFetch } from "@/lib/remoteFetch";

export interface TelemetryReading {
  channel: number;
  type: number;
  name: string;
  unit?: string;
  value: unknown;
}

export interface TelemetryData {
  readings: TelemetryReading[];
  raw: string;
}

// autoFetch fires once; otherwise it waits for Request.
export function TelemetryPanel({
  apiBase,
  autoFetch = false,
  errorHint,
}: {
  apiBase: string;
  autoFetch?: boolean;
  // Shown under a failed request — e.g. a sensor only replies to clients in its ACL.
  errorHint?: string;
}) {
  const [data, setData] = useState<TelemetryData | null>(null);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const fetchedRef = useRef(false);
  const remoteFetch = useRemoteFetch();

  const refresh = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      const r = await remoteFetch(`${apiBase}/telemetry`);
      if (!r.ok) throw new Error(await apiErrorMessage(r));
      const body: TelemetryData = await r.json();
      setData(body);
      fetchedRef.current = true;
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Failed");
    } finally {
      setLoading(false);
    }
  }, [apiBase, remoteFetch]);

  useEffect(() => {
    if (autoFetch && !fetchedRef.current) {
      refresh();
    }
  }, [autoFetch, refresh]);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <span className="label-overline">telemetry · cayenne lpp</span>
        <Button
          variant="outline"
          size="sm"
          onClick={refresh}
          disabled={loading}
          className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
        >
          <RefreshCw className={cn("size-3", loading && "animate-spin")} />
          {data ? "refresh" : "request"}
        </Button>
      </div>
      {err && (
        <Alert variant="destructive">
          <AlertTitle className="font-mono uppercase tracking-widest">
            Error
          </AlertTitle>
          <AlertDescription>
            {err}
            {errorHint && (
              <span className="block mt-1 text-muted-foreground">{errorHint}</span>
            )}
          </AlertDescription>
        </Alert>
      )}
      {!data && loading && (
        <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-px bg-border border border-border">
          {[...Array(4)].map((_, i) => (
            <div key={i} className="bg-card p-4 space-y-2">
              <Skeleton className="h-3 w-20" />
              <Skeleton className="h-6 w-16" />
            </div>
          ))}
        </div>
      )}
      {data && data.readings.length === 0 && !loading && (
        <div className="panel py-10 text-center font-mono text-xs uppercase tracking-[0.12em] text-muted-foreground/60">
          no telemetry returned
        </div>
      )}
      {data && data.readings.length > 0 && (
        <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-px bg-border border border-border">
          {data.readings.map((reading, idx) => (
            <TelemetryTile key={`${reading.channel}-${reading.type}-${idx}`} reading={reading} />
          ))}
        </div>
      )}
    </div>
  );
}

function TelemetryTile({ reading }: { reading: TelemetryReading }) {
  const { name, unit, channel, value } = reading;
  // Composite readings use capitalized keys (GPS Latitude/Longitude, X/Y/Z, R/G/B).
  let display: string;
  let sub: string | null = null;
  let compact = false;
  let coord: { lat: number; lon: number } | null = null;
  if (typeof value === "number") {
    const abs = Math.abs(value);
    const decimals = abs >= 100 ? 0 : abs >= 10 ? 1 : 2;
    display = value.toFixed(decimals);
  } else if (value && typeof value === "object") {
    compact = true;
    const obj = value as Record<string, number>;
    if ("Latitude" in obj && "Longitude" in obj) {
      coord = { lat: obj.Latitude, lon: obj.Longitude };
      display = `${obj.Latitude.toFixed(5)}, ${obj.Longitude.toFixed(5)}`;
      if (typeof obj.Altitude === "number") sub = `alt ${obj.Altitude.toFixed(0)} m`;
    } else if ("X" in obj && "Y" in obj && "Z" in obj) {
      display = `${obj.X.toFixed(2)}, ${obj.Y.toFixed(2)}, ${obj.Z.toFixed(2)}`;
    } else if ("R" in obj && "G" in obj && "B" in obj) {
      display = `${obj.R}, ${obj.G}, ${obj.B}`;
    } else {
      display = JSON.stringify(value);
    }
  } else {
    display = String(value);
  }

  return (
    <div className="bg-card relative px-4 py-3 flex flex-col gap-1.5">
      <div className="flex items-center justify-between gap-2">
        <span className="label-overline truncate">{name}</span>
        <span className="font-mono text-[9px] uppercase tracking-[0.12em] text-muted-foreground/60">
          ch{channel}
        </span>
      </div>
      <div className="flex items-baseline gap-1">
        {coord ? (
          <CoordLink
            lat={coord.lat}
            lon={coord.lon}
            raw={display}
            className="font-mono text-sm font-semibold tabular-nums wrap-break-word"
          />
        ) : (
          <span
            className={cn(
              "font-mono font-semibold tabular-nums leading-none",
              compact ? "text-sm wrap-break-word" : "text-lg break-all",
            )}
          >
            {display}
          </span>
        )}
        {unit && (
          <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
            {unit}
          </span>
        )}
      </div>
      {sub && (
        <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/70">
          {sub}
        </span>
      )}
    </div>
  );
}
