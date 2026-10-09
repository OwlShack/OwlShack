import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useParams } from "react-router-dom";
import { ShieldAlert, Smartphone, Unplug } from "lucide-react";
import { toast } from "sonner";
import { BackLink } from "@/components/BackLink";
import { PageHeader } from "@/components/PageHeader";
import { LoadErrorAlert } from "@/components/LoadErrorAlert";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useCompanionRef } from "@/hooks/useCompanions";
import { apiErrorMessage } from "@/lib/apiError";
import { APP_PORT_FIRST, APP_PORT_LAST, configApi, type ConfigCompanion } from "@/lib/configApi";

const PORTS = Array.from({ length: APP_PORT_LAST - APP_PORT_FIRST + 1 }, (_, i) => APP_PORT_FIRST + i);

// What the running companion's port is doing; it is only open while the radio is up.
interface AppStatus {
  enabled: boolean;
  port?: number;
  listening: boolean;
  error?: string;
  client?: string;
  since?: string;
}

type Action = "on" | "move" | "off";

const hint = "font-mono text-[11px] sm:text-[10px] leading-relaxed text-muted-foreground/70";

async function loadCompanions(): Promise<ConfigCompanion[]> {
  const r = await fetch("/api/config/companions");
  if (!r.ok) throw new Error(await apiErrorMessage(r, "Failed to load app access"));
  return (await r.json()) as ConfigCompanion[];
}

export function CompanionAppPage() {
  const { ref } = useParams();
  const { ref: companion, id, name } = useCompanionRef(ref);

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-3">
        <BackLink to={`/companions/${encodeURIComponent(companion)}`} label={name || "companion"} />
        <PageHeader eyebrow="MeshCore app over TCP" title="App access" className="mb-0" />
      </div>
      {id == null ? (
        <p className={`panel px-4 py-4 ${hint}`}>Loading this companion...</p>
      ) : (
        <AppAccess companionId={id} companionRef={companion} companionName={name} />
      )}
    </div>
  );
}

function AppAccess({
  companionId,
  companionRef,
  companionName,
}: {
  companionId: number;
  companionRef: string;
  companionName: string;
}) {
  const [companions, setCompanions] = useState<ConfigCompanion[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [port, setPort] = useState<number | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState<Action | null>(null);

  const load = useCallback(async () => {
    setLoadError(null);
    try {
      setCompanions(await loadCompanions());
    } catch (e) {
      setLoadError(e instanceof Error ? e.message : "Failed to load app access");
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  const self = companions?.find((c) => c.id === companionId) ?? null;
  // Another companion's open port is taken; one kept while its access is off is not.
  const taken = useMemo(
    () => new Map((companions ?? []).filter((c) => c.id !== companionId && c.app.enabled).map((c) => [c.app.port, c.name])),
    [companions, companionId],
  );
  const firstFree = PORTS.find((p) => !taken.has(p)) ?? null;
  const keptPortTaken = self != null && !self.app.enabled && self.app.port !== 0 && taken.has(self.app.port);
  const savedPort = self && self.app.port !== 0 && !keptPortTaken ? self.app.port : firstFree;
  const chosen = port ?? savedPort;

  const save = async (action: Action, p: number) => {
    setBusy(action);
    try {
      // A move only keeps a port open; if access went off elsewhere, turning it back on needs the warning first.
      if (action === "move") {
        const fresh = (await loadCompanions()).find((c) => c.id === companionId);
        if (!fresh?.app.enabled) {
          toast.error("App access was turned off elsewhere. Turn it on again to choose a port.");
          return;
        }
      }
      await configApi.setCompanionApp(companionId, { enabled: action !== "off", port: p });
      toast.success(action === "off" ? "MeshCore app access off" : `MeshCore app access on, port ${p}`);
      setConfirming(false);
      setPort(null);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save app access");
    } finally {
      setBusy(null);
      await load();
    }
  };

  if (loadError) return <LoadErrorAlert message={loadError} onRetry={() => void load()} />;
  if (companions == null) return <Skeleton className="h-40 w-full" />;
  if (!self) return <LoadErrorAlert message="This companion is no longer configured." onRetry={() => void load()} />;
  const on = self.app.enabled;

  return (
    <>
      <section className="panel">
        <header className="border-b border-border px-4 py-3">
          <h2 className="font-mono text-sm uppercase tracking-widest">Access</h2>
          <p className={hint}>
            Lets the official MeshCore app use this companion over your network, as it would a WiFi radio:
            chat, contacts, channels, logging in to repeaters and rooms, and the companion's own settings. The
            radio's settings stay here.
          </p>
        </header>

        <div className="space-y-3 px-4 py-3">
          {on ? (
            <div className="flex items-start gap-2 border border-destructive/40 bg-destructive/5 px-3 py-2">
              <ShieldAlert className="mt-0.5 size-3.5 shrink-0 text-destructive" />
              <p className="font-mono text-[11px] leading-relaxed text-destructive sm:text-[10px]">
                The app has no password. Anyone who can reach port {self.app.port} can read this companion's
                messages, contacts and channel keys, and send as it.
              </p>
            </div>
          ) : null}

          <div className="flex flex-wrap items-end gap-3">
            <div className="min-w-0 space-y-1">
              <Label htmlFor="app-port" className="label-overline">
                Port
              </Label>
              <Select
                value={chosen == null ? "" : String(chosen)}
                onValueChange={(v) => setPort(Number(v))}
                disabled={busy !== null || firstFree == null}
              >
                <SelectTrigger id="app-port" className="w-56 rounded-none border-border bg-background font-mono text-xs">
                  <SelectValue placeholder="every port is taken" />
                </SelectTrigger>
                <SelectContent className="rounded-none font-mono text-xs">
                  {PORTS.map((p) => (
                    <SelectItem key={p} value={String(p)} disabled={taken.has(p)} className="rounded-none font-mono text-xs">
                      {p}
                      {taken.has(p) ? ` (${taken.get(p)})` : ""}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            {on ? (
              <>
                {chosen != null && chosen !== self.app.port ? (
                  <Button size="xs" onClick={() => void save("move", chosen)} disabled={busy !== null}>
                    {busy === "move" ? "Moving..." : `Move to ${chosen}`}
                  </Button>
                ) : null}
                <Button size="xs" variant="outline" onClick={() => void save("off", self.app.port)} disabled={busy !== null}>
                  {busy === "off" ? "Turning off..." : "Turn off"}
                </Button>
              </>
            ) : (
              <Button size="xs" onClick={() => setConfirming(true)} disabled={busy !== null || chosen == null}>
                <Smartphone />
                Turn on
              </Button>
            )}
          </div>

          {firstFree == null && !on ? (
            <p className={hint}>
              Every port from {APP_PORT_FIRST} to {APP_PORT_LAST} is in use by another companion.
            </p>
          ) : null}
          {keptPortTaken ? (
            <p className={hint}>
              Port {self.app.port} is now used by {taken.get(self.app.port)}, so another is offered.
            </p>
          ) : null}
          {on && chosen !== self.app.port ? <p className={hint}>Moving the port drops a connected app.</p> : null}
        </div>
      </section>

      {on ? <Connection companionRef={companionRef} port={self.app.port} onStale={load} /> : null}

      <Dialog open={confirming} onOpenChange={(o) => busy === null && setConfirming(o)}>
        <DialogContent className="rounded-none border-border bg-card sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="font-mono text-sm uppercase tracking-[0.08em]">Let the MeshCore app in?</DialogTitle>
            <DialogDescription className="text-xs leading-relaxed text-muted-foreground">
              The MeshCore app has no password. Anyone who can reach port {chosen} on this machine can read{" "}
              {companionName || "this companion"}'s messages, contacts and channel keys, and send as it. Turn this on
              only on a network you trust, and keep the port off the internet.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button size="xs" variant="outline" onClick={() => setConfirming(false)} disabled={busy !== null}>
              Cancel
            </Button>
            <Button
              size="xs"
              variant="destructive"
              onClick={() => chosen != null && void save("on", chosen)}
              disabled={busy !== null || chosen == null}
            >
              {busy === "on" ? "Turning on..." : "Turn on"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

// Connection reads the live port every few seconds: nothing here touches the radio.
function Connection({ companionRef, port, onStale }: { companionRef: string; port: number; onStale: () => void }) {
  const [status, setStatus] = useState<AppStatus | null>(null);
  const [error, setError] = useState<{ message: string; radioDown: boolean } | null>(null);
  const [busy, setBusy] = useState(false);
  const seq = useRef(0);
  const base = `/api/companions/${encodeURIComponent(companionRef)}/app`;

  const read = useCallback(async () => {
    if (document.visibilityState === "hidden") return;
    const id = ++seq.current;
    try {
      const r = await fetch(base);
      if (!r.ok) {
        const message = await apiErrorMessage(r, "Failed to read the app port");
        if (seq.current === id) setError({ message, radioDown: r.status === 404 || r.status === 503 });
        return;
      }
      const s = (await r.json()) as AppStatus;
      if (seq.current !== id) return;
      setStatus(s);
      setError(null);
    } catch (e) {
      if (seq.current === id) setError({ message: e instanceof Error ? e.message : "Failed to read the app port", radioDown: false });
    }
  }, [base]);

  useEffect(() => {
    void read();
    const t = window.setInterval(() => void read(), 5000);
    return () => {
      window.clearInterval(t);
      seq.current++;
    };
  }, [read, port]);

  // The node says access is off while this page says on: someone else changed it, or the save never applied.
  const stale = status != null && !status.enabled;
  const staleSeen = useRef(false);
  useEffect(() => {
    if (stale && !staleSeen.current) {
      staleSeen.current = true;
      onStale();
    }
    if (!stale) staleSeen.current = false;
  }, [stale, onStale]);

  const disconnect = async () => {
    setBusy(true);
    try {
      const r = await fetch(`${base}/disconnect`, { method: "POST" });
      if (!r.ok) throw new Error(await apiErrorMessage(r, "Failed to disconnect the app"));
      toast.success("App disconnected");
      await read();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to disconnect the app");
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="panel">
      <header className="border-b border-border px-4 py-3">
        <h2 className="font-mono text-sm uppercase tracking-widest">Connection</h2>
        <p className={hint}>
          In the MeshCore app, connect over TCP to this machine's address on your network, port{" "}
          <span className="text-foreground">{port}</span>. The port listens on the same address as this page, so a
          page kept to localhost keeps the port there too. In Docker, publish the ports with{" "}
          <code className="whitespace-nowrap text-foreground">-p {APP_PORT_FIRST}-{APP_PORT_LAST}:{APP_PORT_FIRST}-{APP_PORT_LAST}</code>.
        </p>
      </header>
      <div className="flex flex-wrap items-center gap-3 px-4 py-3 font-mono text-xs">
        {error ? (
          <span className="text-muted-foreground">
            {error.message}
            {error.radioDown ? ". The port is open only while the radio is running." : ""}
          </span>
        ) : status == null ? (
          <Skeleton className="h-4 w-48" />
        ) : !status.enabled ? (
          <span className="text-muted-foreground">Opening the port...</span>
        ) : status.port !== port ? (
          <span className="text-muted-foreground">Moving to port {port}...</span>
        ) : status.error ? (
          <span className="text-destructive">Port {status.port} could not be opened: {status.error}</span>
        ) : status.client ? (
          <>
            <span>
              Connected from <span className="text-foreground">{status.client}</span>
              {status.since ? (
                <span className="text-muted-foreground"> since {new Date(status.since).toLocaleString()}</span>
              ) : null}
            </span>
            <Button size="xs" variant="outline" onClick={() => void disconnect()} disabled={busy}>
              <Unplug />
              {busy ? "Disconnecting..." : "Disconnect"}
            </Button>
          </>
        ) : (
          <span className="text-muted-foreground">Listening on port {status.port}. No app connected.</span>
        )}
      </div>
    </section>
  );
}
