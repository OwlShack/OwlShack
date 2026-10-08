import { useState } from "react";
import { Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { SelectField, TextField } from "@/components/ConfigFields";
import { InlineConfirm } from "@/components/InlineConfirm";
import { RegionSelect } from "@/components/RegionSelect";
import { RegionName, UnderSelect, regionNameError, regionTree, underLabel, type RegionNode } from "@/components/RegionTree";
import type { FloodScope } from "@/lib/configApi";

export interface EditableRegion {
  name: string;
  parent: string;
  denyFlood: boolean;
}

// The firmware finds a region by name with any leading "#" ignored, so "#akl" is akl.
const bare = (n: string) => n.replace(/^#/, "");

// RegionListEditor is a repeater's regions as on firmware: "*" first, then the tree, with its home region; each change resolves to whether it took.
export function RegionListEditor({
  regions,
  home,
  onHome,
  scope,
  onMove,
  onDeny,
  onRemove,
  onAdd,
  busy = false,
}: {
  regions: EditableRegion[];
  home: string;
  onHome: (name: string) => Promise<boolean>;
  // scope is the firmware's default region: its value, the regions it may be, how it saves, and whether the node has one.
  scope: { value: FloodScope; choices: RegionNode[]; onChange: (v: FloodScope) => void; hint: string; unsupported?: boolean };
  onMove: (name: string, parent: string) => Promise<boolean>;
  onDeny: (name: string, deny: boolean) => Promise<boolean>;
  onRemove: (name: string) => Promise<boolean>;
  onAdd: (name: string, parent: string) => Promise<boolean>;
  busy?: boolean;
}) {
  const [confirm, setConfirm] = useState<string | null>(null);
  const [newRegion, setNewRegion] = useState("");
  const [newParent, setNewParent] = useState("*");

  const wildcard = regions.find((r) => r.name === "*") ?? { name: "*", parent: "", denyFlood: false };
  const rows = [{ region: wildcard, depth: 0, under: [] as string[] }, ...regionTree(regions)];
  const rn = newRegion.trim();
  const duplicate = rn !== "" && regions.some((r) => bare(r.name) === bare(rn));
  const invalid = rn === "" ? null : regionNameError(rn, true);
  const parent = regions.some((r) => r.name === newParent) ? newParent : "*";
  const add = async () => {
    if (busy || rn === "" || duplicate || invalid) return;
    if (await onAdd(rn, parent)) setNewRegion("");
  };

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <RegionSelect
          label="Region for its own adverts and replies"
          value={scope.value}
          onChange={scope.onChange}
          regions={scope.choices}
          disabled={busy || scope.unsupported}
          hint={scope.hint}
        />
        <SelectField
          label="Home region"
          value={regions.some((r) => r.name === home) ? home : "*"}
          onChange={(v) => void onHome(v)}
          disabled={busy}
          options={rows.map(({ region, depth }) => ({
            value: region.name,
            label: region.name === "*" ? "none" : region.name,
            depth,
          }))}
          hint="the region this repeater is in; other nodes can see it, and nothing routes on it · firmware region home"
        />
      </div>
      <div className="divide-y divide-border border border-border">
        {rows.map(({ region: rg, depth, under }) => (
          <div key={rg.name} className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 px-3 py-2">
            {rg.name === "*" ? (
              <span className="flex min-w-0 items-baseline gap-2 font-mono text-sm">
                *<span className="text-[11px] text-muted-foreground">traffic sent with no region</span>
              </span>
            ) : (
              <RegionName name={rg.name} depth={depth} />
            )}
            <div className="ml-auto flex flex-wrap items-center justify-end gap-x-2 gap-y-1 sm:gap-x-4">
              {rg.name !== "*" && (
                <UnderSelect
                  name={rg.name}
                  value={rg.parent}
                  options={under}
                  disabled={busy}
                  onChange={(p) => void onMove(rg.name, p)}
                />
              )}
              <label className="flex items-center gap-2 whitespace-nowrap font-mono text-[10px] uppercase tracking-[0.08em] text-muted-foreground">
                deny flood
                <Switch
                  checked={rg.denyFlood}
                  disabled={busy}
                  aria-label={`deny flood in ${rg.name}`}
                  onCheckedChange={() => void onDeny(rg.name, !rg.denyFlood)}
                />
              </label>
              {rg.name !== "*" && (
                <InlineConfirm
                  confirming={confirm === rg.name}
                  onAskRemove={() => setConfirm(rg.name)}
                  onCancel={() => setConfirm(null)}
                  onConfirm={() => {
                    setConfirm(null);
                    void onRemove(rg.name);
                  }}
                  iconOnly
                  disabled={busy}
                  ariaLabel={`remove region ${rg.name}`}
                  blockedReason={regions.some((r) => r.parent === rg.name) ? "move or remove its sub-regions first" : undefined}
                />
              )}
            </div>
          </div>
        ))}
      </div>
      <form
        className="space-y-1"
        onSubmit={(e) => {
          e.preventDefault();
          void add();
        }}
      >
        <div className="flex flex-wrap items-end gap-2">
          <div className="min-w-48 flex-1">
            <TextField label="Add region" value={newRegion} onChange={setNewRegion} placeholder="region name" disabled={busy} />
          </div>
          <div className="w-40">
            <SelectField
              label="Under"
              value={parent}
              onChange={setNewParent}
              options={rows.map(({ region, depth }) => ({
                value: region.name,
                label: region.name === "*" ? underLabel("*") : region.name,
                depth,
              }))}
            />
          </div>
          <Button
            type="submit"
            variant="outline"
            disabled={busy || rn === "" || duplicate || invalid !== null}
            className="h-9 rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
          >
            <Plus className="size-3.5" />
            add
          </Button>
        </div>
        <p className={`font-mono text-[10px] ${invalid ? "text-destructive" : "text-muted-foreground/60"}`}>
          {invalid ?? (duplicate ? "already in the list" : "the transport key derives from this name")}
        </p>
      </form>
    </div>
  );
}
