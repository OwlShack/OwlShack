import { useCallback, useEffect, useRef, useState } from "react";
import { RefreshCw, Save } from "lucide-react";
import { toast } from "sonner";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { RegionListEditor, type EditableRegion } from "@/components/RegionListEditor";
import { regionName } from "@/components/RegionSelect";
import { NESTING_HINT } from "@/components/RegionTree";
import { apiErrorMessage } from "@/lib/apiError";
import type { FloodScope } from "@/lib/configApi";
import { cn } from "@/lib/utils";
import { useRemoteFetch } from "@/lib/remoteFetch";

// RemoteRegionMap is GET .../regions: the node's regions as its CLI reports them, names without "#".
interface RemoteRegionMap {
  regions: EditableRegion[];
  home: string; // "*" when none is set
  default: string; // "" for none
  defaultSupported: boolean;
  partial: boolean;
}

const bare = (n: string) => n.replace(/^#/, "");

// RemoteRegionsTab edits a remote node's regions as the MeshCore app does: each change goes out at once, and Save writes them to its flash.
export function RemoteRegionsTab({
  apiBase,
  sendCli,
  active,
}: {
  apiBase: string;
  sendCli: (cmd: string) => Promise<string>;
  active: boolean;
}) {
  const [map, setMap] = useState<RemoteRegionMap | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const busyRef = useRef(false);
  // The node keeps unsaved changes in memory, so the marker outlives this tab until Save; a reload of the list doesn't clear it.
  const unsavedKey = `regions-unsaved:${apiBase}`;
  const [unsaved, setUnsavedState] = useState(() => {
    try {
      return sessionStorage.getItem(unsavedKey) === "1";
    } catch {
      return false;
    }
  });
  const setUnsaved = (v: boolean) => {
    setUnsavedState(v);
    try {
      if (v) sessionStorage.setItem(unsavedKey, "1");
      else sessionStorage.removeItem(unsavedKey);
    } catch {
      // the marker then lasts only while this tab is open
    }
  };
  const fetchedRef = useRef(false);
  const remoteFetch = useRemoteFetch();

  const load = useCallback(async () => {
    fetchedRef.current = true;
    setLoading(true);
    setError(null);
    try {
      const r = await remoteFetch(`${apiBase}/regions`);
      if (!r.ok) throw new Error(await apiErrorMessage(r, "Reading the regions failed"));
      setMap((await r.json()) as RemoteRegionMap);
    } catch (e) {
      setError(`${e instanceof Error ? e.message : "Reading the regions failed"}. Check the node is in range, then press Refresh.`);
    } finally {
      setLoading(false);
    }
  }, [apiBase, remoteFetch]);

  useEffect(() => {
    if (active && !fetchedRef.current) void load();
  }, [active, load]);

  // act runs one action at a time, its commands included, so nothing else can slip in between them.
  const act = async <T,>(fn: () => Promise<T>, idle: T): Promise<T> => {
    if (busyRef.current) return idle;
    busyRef.current = true;
    setBusy(true);
    try {
      return await fn();
    } finally {
      busyRef.current = false;
      setBusy(false);
    }
  };
  // run sends one command. Only a 504 (or no answer from OwlShack) may have reached the node, so only a change then needs checking; anything else was refused.
  const run = async (cmd: string, ok: (reply: string) => boolean, read = false): Promise<string | null> => {
    let reply: string;
    try {
      reply = (await sendCli(cmd)).trim();
    } catch (e) {
      const status = (e as { status?: number }).status;
      const msg = e instanceof Error ? e.message : "failed";
      if (status !== undefined && status !== 504) {
        toast.error(`${cmd}: ${msg}`);
      } else if (read) {
        toast.error(`${cmd}: no reply (${msg}). Try again.`);
      } else {
        setUnsaved(true);
        toast.error(`${cmd}: no reply (${msg}). It may have reached the node: press Refresh to check.`);
      }
      return null;
    }
    if (!ok(reply)) {
      toast.error(`${cmd}: ${reply || "refused"}`);
      return null;
    }
    return reply;
  };
  const isOK = (r: string) => r.startsWith("OK");
  const change = (next: (m: RemoteRegionMap) => RemoteRegionMap) => {
    setMap((m) => m && next(m));
    setUnsaved(true);
  };
  const withRegion = (m: RemoteRegionMap, name: string, edit: Partial<EditableRegion>) => ({
    ...m,
    regions: m.regions.map((r) => (r.name === name ? { ...r, ...edit } : r)),
  });

  const onAdd = (name: string, parent: string) =>
    act(async () => {
      // A cut list may hide a region of this name, and a put on it would move it.
      if (map?.partial) {
        const got = await run(`region get ${name}`, () => true, true);
        if (got === null) return false;
        // A found region answers " name (parent) F"; not found is "Err - unknown region".
        if (!got.startsWith("Err - ") && bare(got.split(/\s/)[0] ?? "") === bare(name)) {
          toast.error(`${bare(name)} is already on the node; press Refresh to see it`);
          return false;
        }
      }
      const reply = await run(`region put ${name} ${parent}`, isOK);
      if (reply === null) return false;
      // Firmware 1.15+ allows flood on a put and says so; older keeps a new region denied.
      change((m) => ({ ...m, regions: [...m.regions, { name: bare(name), parent, denyFlood: !reply.includes("(flood allowed)") }] }));
      return true;
    }, false);
  const onMove = (name: string, parent: string) =>
    act(async () => {
      const was = map?.regions.find((r) => r.name === name);
      const reply = await run(`region put ${name} ${parent}`, isOK);
      if (reply === null) return false;
      change((m) => withRegion(m, name, { parent, denyFlood: reply.includes("(flood allowed)") ? false : (was?.denyFlood ?? true) }));
      // A put allows flood again, so a denied region is denied again after the move.
      if (was?.denyFlood && reply.includes("(flood allowed)") && (await run(`region denyf ${name}`, isOK)) !== null) {
        change((m) => withRegion(m, name, { denyFlood: true }));
      }
      return true;
    }, false);
  const onDeny = (name: string, deny: boolean) =>
    act(async () => {
      if ((await run(`region ${deny ? "denyf" : "allowf"} ${name}`, isOK)) === null) return false;
      change((m) => withRegion(m, name, { denyFlood: deny }));
      return true;
    }, false);
  const onRemove = (name: string) =>
    act(async () => {
      if ((await run(`region remove ${name}`, isOK)) === null) return false;
      // The firmware's home and default then find no region.
      change((m) => ({
        ...m,
        regions: m.regions.filter((r) => r.name !== name),
        home: m.home === name ? "*" : m.home,
        default: m.default === name ? "" : m.default,
      }));
      return true;
    }, false);
  const onHome = (name: string) =>
    act(async () => {
      if ((await run(`region home ${name}`, (r) => r.includes("home is now"))) === null) return false;
      change((m) => ({ ...m, home: name }));
      return true;
    }, false);
  const onDefault = (scope: FloodScope) =>
    act(async () => {
      const name = regionName(scope) ?? "";
      if ((await run(`region default ${name || "<null>"}`, (r) => r.includes("default scope is now"))) === null) return;
      // It allows flood on the region and saves the whole map.
      setMap((m) => m && { ...(name ? withRegion(m, name, { denyFlood: false }) : m), default: name });
      setUnsaved(false);
    }, undefined);
  const save = () =>
    act(async () => {
      if ((await run("region save", isOK)) !== null) {
        setUnsaved(false);
        toast.success("Regions saved on the node");
      }
    }, undefined);

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="label-overline">
          regions
          <span role="status" className="ml-2 normal-case tracking-normal text-warning">
            {unsaved ? "· not saved: a reboot will undo these changes. Press Save." : ""}
          </span>
        </span>
        <div className="flex items-center gap-2">
          <Button
            variant={unsaved ? "default" : "outline"}
            size="sm"
            onClick={save}
            disabled={busy || loading || !map}
            className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
          >
            <Save className="size-3" /> save
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={load}
            disabled={busy || loading}
            className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
          >
            <RefreshCw className={cn("size-3", loading && "animate-spin")} />
            refresh
          </Button>
        </div>
      </div>
      {error && (
        <Alert variant="destructive">
          <AlertTitle className="font-mono uppercase tracking-widest">Error</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {map ? (
        <div className="panel space-y-4 p-4">
          <p className="font-mono text-[11px] leading-relaxed text-muted-foreground">
            The regions this node relays: it re-floods scoped packets whose region matches one of these. The{" "}
            <span className="text-foreground">*</span> row is traffic sent with no region; deny flood on it to stop
            relaying that. Each change is sent straight away; Save keeps them over a reboot. {NESTING_HINT}
          </p>
          {map.partial && (
            <p role="status" className="font-mono text-[11px] text-warning">
              This node holds more regions than its console can list, so some may be missing here.
            </p>
          )}
          <RegionListEditor
            regions={map.regions}
            home={map.home}
            onHome={onHome}
            scope={{
              value: map.default ? `region:${map.default}` : "everywhere",
              choices: map.regions.filter((r) => r.name !== "*"),
              onChange: (v) => void onDefault(v),
              unsupported: !map.defaultSupported,
              hint: map.defaultSupported
                ? "its flood adverts, and replies it can't scope to the request; picking one allows flood on it, and keeps all your changes over a reboot as Save does · firmware region default"
                : "this firmware has no region default",
            }}
            onMove={onMove}
            onDeny={onDeny}
            onRemove={onRemove}
            onAdd={onAdd}
            busy={busy || loading}
          />
        </div>
      ) : (
        !error && (
          <div className="panel py-10 text-center font-mono text-xs uppercase tracking-[0.12em] text-muted-foreground/60">
            {loading ? "reading the regions…" : "no regions read yet"}
          </div>
        )
      )}
    </div>
  );
}
