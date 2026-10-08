import { useEffect, useState } from "react";
import { Check, Copy, Loader2, Plus, Radar, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { SectionTitle } from "@/components/SectionTitle";
import { SelectField, TextField } from "@/components/ConfigFields";
import { RegionSelect } from "@/components/RegionSelect";
import { NESTING_HINT, RegionName, UnderSelect, regionNameError, regionTree, underLabel } from "@/components/RegionTree";
import type { ConfigRepeater, FloodRegion, FloodScope } from "@/lib/configApi";
import { useApiObject } from "@/hooks/useApiObject";
import { regionScanApi, type RegionScanState } from "@/lib/discoverApi";

const bare = (name: string) => name.replace(/^#/, "");

// The Settings region list and default; edits are saved with the page.
export function FloodRegionsSection({
  regions,
  onRegionsChange,
  scope,
  onScopeChange,
}: {
  regions: FloodRegion[];
  onRegionsChange: (r: FloodRegion[]) => void;
  scope: FloodScope;
  onScopeChange: (s: FloodScope) => void;
}) {
  const [draft, setDraft] = useState("");
  const [draftParent, setDraftParent] = useState("*");
  const { item: repeater } = useApiObject<ConfigRepeater>("/api/config/repeater", "Could not load your repeater");
  const names = regions.map((r) => r.name);
  const rows = regionTree(regions);
  // A "$" region needs keys we can't load, so it can't be sent in.
  const repeaterRegions = (repeater?.configured ? repeater.regions : []).filter(
    (r) => r.name !== "*" && !r.name.startsWith("$"),
  );
  const toCopy = repeaterRegions.map((r) => ({ name: bare(r.name), parent: bare(r.parent) })).filter((r) => !names.includes(r.name));
  // The firmware hashes "#name"; people often type the hash themselves.
  const name = draft.trim().replace(/^#/, "");
  const duplicate = names.includes(name);
  const invalid = name === "" ? null : regionNameError(name);

  const add = () => {
    if (name === "" || duplicate || invalid) return;
    onRegionsChange([...regions, { name, parent: names.includes(draftParent) ? draftParent : "*" }]);
    setDraft("");
  };
  const move = (n: string, parent: string) => onRegionsChange(regions.map((r) => (r.name === n ? { ...r, parent } : r)));
  // Its sub-regions move up to where it sat, so removing one never strands the others.
  const remove = (gone: FloodRegion) =>
    onRegionsChange(
      regions.filter((r) => r.name !== gone.name).map((r) => (r.parent === gone.name ? { ...r, parent: gone.parent } : r)),
    );
  const copy = () => {
    const all = new Set([...names, ...toCopy.map((r) => r.name)]);
    onRegionsChange([...regions, ...toCopy.map((r) => ({ ...r, parent: all.has(r.parent) ? r.parent : "*" }))]);
  };

  return (
    <section className="panel">
      <SectionTitle eyebrow="scoping" title="Regions" />
      <div className="space-y-4 p-4">
        <p className="max-w-prose font-mono text-[11px] leading-relaxed text-muted-foreground">
          A region keeps a flood to the repeaters that carry it. Every
          companion, channel, contact and bot can pick one of these, or use
          the same as the level above. Names must match the mesh exactly: nz
          and NZ are different regions. {NESTING_HINT}
        </p>
        <div className="sm:max-w-xs">
          <RegionSelect
            label="Default region"
            value={scope}
            onChange={onScopeChange}
            regions={regions}
            hint="what everything set to the same as above ends up using"
          />
        </div>
        {rows.length > 0 && (
          <ul className="divide-y divide-border border border-border">
            {rows.map(({ region, depth, under }) => (
              <li key={region.name} className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 px-3 py-1.5">
                <RegionName name={region.name} depth={depth} />
                <div className="ml-auto flex items-center gap-2">
                  <UnderSelect name={region.name} value={region.parent} options={under} onChange={(p) => move(region.name, p)} />
                  <Button
                    size="icon"
                    variant="ghost"
                    className="size-8 rounded-none"
                    aria-label={`remove region ${region.name}`}
                    onClick={() => remove(region)}
                  >
                    <X className="size-3.5" />
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}
        <form
          className="space-y-1"
          onSubmit={(e) => {
            e.preventDefault();
            add();
          }}
        >
          <div className="flex flex-wrap items-end gap-2">
            <div className="min-w-48 flex-1">
              <TextField label="Add region" value={draft} onChange={setDraft} placeholder="e.g. nz-wlg" />
            </div>
            <div className="w-40">
              <SelectField
                label="Under"
                value={names.includes(draftParent) ? draftParent : "*"}
                onChange={setDraftParent}
                options={[
                  { value: "*", label: underLabel("*") },
                  ...rows.map(({ region, depth }) => ({ value: region.name, label: region.name, depth })),
                ]}
              />
            </div>
            <Button
              type="submit"
              variant="outline"
              disabled={name === "" || duplicate || invalid !== null}
              className="h-9 rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
            >
              <Plus className="size-3.5" />
              add
            </Button>
          </div>
          <p className={`font-mono text-[10px] ${invalid ? "text-destructive" : "text-muted-foreground/60"}`}>
            {invalid ?? (duplicate && name !== "" ? "already in the list" : "as your local mesh names it")}
          </p>
        </form>
        <DiscoverNearby
          listed={names}
          onAdd={(found) => onRegionsChange([...regions, ...found.map((n) => ({ name: n, parent: "*" }))])}
        />
        {repeaterRegions.length > 0 && (
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <Button
              variant="outline"
              size="sm"
              disabled={toCopy.length === 0}
              onClick={copy}
              className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
            >
              <Copy className="size-3.5" />
              add my repeater's regions
            </Button>
            <span className="font-mono text-[10px] text-muted-foreground">
              {toCopy.length === 0
                ? "all of them are listed"
                : `adds ${toCopy.map((r) => r.name).join(", ")}, nested as on your repeater`}
            </span>
          </div>
        )}
      </div>
    </section>
  );
}

// sendable drops "*" (unscoped, not a region) and "$" regions, whose keys we can't load.
const sendable = (name: string) => name !== "*" && !name.startsWith("$");

// DiscoverNearby asks the repeaters in radio range which regions they flood, and offers each name found.
function DiscoverNearby({ listed, onAdd }: { listed: string[]; onAdd: (names: string[]) => void }) {
  const [scan, setScan] = useState<RegionScanState | null>(null);
  const running = scan?.running ?? false;

  // Picks up a run still going when the page is reopened, then polls only while it runs.
  useEffect(() => {
    let stop = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = () =>
      regionScanApi
        .state()
        .then((st) => {
          if (stop) return;
          setScan(st);
          if (st.running) timer = setTimeout(poll, 1000);
        })
        .catch(() => {
          // A dropped poll (Wi-Fi blip, phone asleep) must not freeze a run that is still going.
          if (!stop && running) timer = setTimeout(poll, 3000);
        });
    if (scan === null || running) poll();
    return () => {
      stop = true;
      clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [running]);

  const start = () =>
    regionScanApi
      .start()
      .then(setScan)
      .catch((e) => toast.error(e instanceof Error ? e.message : "Discover nearby failed"));

  const found = new Map<string, string[]>();
  for (const rp of scan?.repeaters ?? []) {
    for (const name of rp.regions.filter(sendable)) {
      found.set(name, [...(found.get(name) ?? []), rp.name || rp.pubkey.slice(0, 8)]);
    }
  }
  const fresh = [...found.keys()].filter((n) => !listed.includes(n));

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <Button
          variant="outline"
          size="sm"
          disabled={running}
          onClick={start}
          className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
        >
          {running ? <Loader2 className="size-3.5 animate-spin" /> : <Radar className="size-3.5" />}
          discover nearby
        </Button>
        <span className="font-mono text-[10px] text-muted-foreground" aria-live="polite">
          {scan?.phase === "listening" ? (
            <>
              listening for repeaters in range<span aria-hidden> · {scan.secsLeft} s</span>
            </>
          ) : scan?.phase === "asking" ? (
            "asking each repeater for its regions"
          ) : scan?.phase === "done" ? (
            `last run ${new Date(scan.startedAt).toLocaleTimeString()}`
          ) : (
            "asks the repeaters that hear you which regions they carry"
          )}
        </span>
      </div>
      {scan && scan.phase !== "" && scan.phase !== "listening" && (
        <>
          {scan.repeaters.length === 0 ? (
            <p className="font-mono text-[11px] text-muted-foreground">No repeater answered the discovery.</p>
          ) : (
            <ul className="divide-y divide-border border border-border">
              {scan.repeaters.map((rp) => {
                const named = rp.regions.filter(sendable);
                return (
                  <li key={rp.pubkey} className="flex flex-wrap items-center justify-between gap-x-3 px-3 py-1.5 font-mono text-[11px]">
                    <span className="min-w-0 truncate text-sm">{rp.name || rp.pubkey.slice(0, 12)}</span>
                    <span className="text-muted-foreground">
                      {rp.status === "answered"
                        ? named.length > 0
                          ? named.join(", ")
                          : "no named regions"
                        : rp.status}
                    </span>
                  </li>
                );
              })}
            </ul>
          )}
          {scan.repeaters.some((rp) => rp.status === "no answer") && (
            <p className="font-mono text-[10px] text-muted-foreground">
              A repeater answers 4 of these every 3 minutes, from anyone, and older firmware not at all.
            </p>
          )}
          {found.size > 0 && (
            <div className="flex flex-wrap items-center gap-2">
              {[...found.entries()].map(([name, from]) => {
                const isListed = listed.includes(name);
                return (
                  <Button
                    key={name}
                    variant="outline"
                    size="sm"
                    disabled={isListed}
                    title={`from ${from.join(", ")}`}
                    onClick={() => onAdd([name])}
                    className="rounded-none font-mono text-xs"
                  >
                    {isListed ? <Check className="size-3.5" /> : <Plus className="size-3.5" />}
                    {name}
                  </Button>
                );
              })}
              {fresh.length > 1 && (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => onAdd(fresh)}
                  className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
                >
                  add all {fresh.length}
                </Button>
              )}
            </div>
          )}
        </>
      )}
    </div>
  );
}
