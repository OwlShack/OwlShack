import { useEffect, useRef } from "react";
import { SelectField } from "@/components/ConfigFields";
import { useApiList } from "@/hooks/useApiList";
import { useApiObject } from "@/hooks/useApiObject";
import type { ConfigChannel, ConfigCompanion, FloodRegion, FloodScope, Settings } from "@/lib/configApi";
import { regionTree } from "@/components/RegionTree";

// useRegionSettings is the Settings region list and default every picker starts from.
export function useRegionSettings(): { regions: FloodRegion[]; scope: FloodScope } {
  const { item } = useApiObject<Settings>("/api/config/settings", "Failed to load regions");
  return { regions: item?.floodRegions ?? [], scope: item?.floodScope ?? "everywhere" };
}

// resolveScope mirrors config.ResolveScope: the most specific level that does not inherit wins.
export function resolveScope(...levels: (FloodScope | undefined)[]): FloodScope {
  return levels.find((s) => s && s !== "inherit") ?? "everywhere";
}

// regionName is the region a scope names, or null for everywhere and inherit.
export function regionName(scope: string | undefined): string | null {
  return scope?.startsWith("region:") ? scope.slice("region:".length) : null;
}

export function scopeLabel(scope: FloodScope): string {
  return regionName(scope) ?? "everywhere";
}

// RegionSelect picks one region from the list in Settings; `inherit` names the level above and what it resolves to, and is left out at the top.
export function RegionSelect({
  label = "Region",
  value,
  onChange,
  regions,
  inherit,
  hint,
  disabled,
}: {
  label?: string;
  value: FloodScope;
  onChange: (v: FloodScope) => void;
  regions: FloodRegion[];
  inherit?: { from: string; resolved?: FloodScope };
  hint?: React.ReactNode;
  disabled?: boolean;
}) {
  const current = regionName(value);
  // A scope from before the list was enforced, or a remote `region default`, need not be in the list; it shows at the top, marked.
  const unlisted = current && !regions.some((r) => r.name === current) ? current : null;
  const tree = regionTree(unlisted ? [...regions, { name: unlisted, parent: "*" }] : regions);
  const options = [
    ...(inherit
      ? [
          {
            value: "inherit",
            label: inherit.resolved
              ? `Same as ${inherit.from} (${scopeLabel(inherit.resolved)})`
              : `Same as ${inherit.from}`,
          },
        ]
      : []),
    { value: "everywhere", label: "Everywhere" },
    ...tree.map(({ region, depth }) => ({
      value: `region:${region.name}`,
      label: region.name === unlisted ? `${region.name} (not listed)` : region.name,
      depth,
    })),
  ];
  return (
    <SelectField
      label={label}
      value={value}
      options={options}
      onChange={(v) => onChange(v as FloodScope)}
      hint={hint}
      disabled={disabled}
    />
  );
}

// useCompanionScope is the region a companion's sends go in when nothing below it says otherwise.
export function useCompanionScope(companionId: number | null): FloodScope {
  const settings = useRegionSettings();
  const { items: companions } = useApiList<ConfigCompanion>("/api/config/companions", "Failed to load companions");
  return resolveScope(companions?.find((c) => c.id === companionId)?.floodScope, settings.scope);
}

// useSendRegion is the region the next send in a thread goes in, resolved as the server does, and whether a DM goes direct (no region) on a known route; refresh re-reads the route.
export function useSendRegion(
  companionId: number | null,
  companionRef: string,
  thread: string | null,
  refresh?: unknown,
  configRefresh?: unknown,
): { scope: FloodScope; direct: boolean; companionScope: FloodScope } {
  const settings = useRegionSettings();
  const dm = thread?.startsWith("dm:") ? thread.slice(3) : null;
  const { items: companions } = useApiList<ConfigCompanion>("/api/config/companions", "Failed to load companions");
  const { items: channels, reload: reloadChannels } = useApiList<ConfigChannel>(
    companionId != null && thread && !dm ? `/api/config/companions/${companionId}/channels` : null,
    "Failed to load channels",
  );
  // A DM to a node that is not a contact 404s here and falls through to the companion's region.
  const { item: contact, notFound: contactNotFound, reload: reloadContact } = useApiObject<{
    peerPubkey: string;
    floodScope: FloodScope;
    outPath: string | null;
  }>(
    dm && companionRef ? `/api/companions/${encodeURIComponent(companionRef)}/contacts/${dm}` : null,
    "Failed to load contact",
  );
  // The hook keeps its last item across a URL change or a failed fetch, so only a contact that is this DM's counts; a fetch that failed some other way keeps it.
  const thisContact = !contactNotFound && contact?.peerPubkey.toLowerCase() === dm?.toLowerCase() ? contact : null;
  // A DM's reply can teach the route, so look again as the thread moves; the first key is the fetch the URL already made.
  const lastRefresh = useRef(refresh);
  useEffect(() => {
    if (lastRefresh.current === refresh) return;
    lastRefresh.current = refresh;
    if (dm) reloadContact();
  }, [dm, refresh, reloadContact]);
  // A region changed elsewhere: read the thread's own setting again.
  const lastConfig = useRef(configRefresh);
  useEffect(() => {
    if (lastConfig.current === configRefresh) return;
    lastConfig.current = configRefresh;
    if (dm) reloadContact();
    else reloadChannels();
  }, [dm, configRefresh, reloadContact, reloadChannels]);
  const own = dm ? thisContact?.floodScope : channels?.find((c) => c.name === thread)?.floodScope;
  const companionScope = resolveScope(companions?.find((c) => c.id === companionId)?.floodScope, settings.scope);
  return {
    scope: resolveScope(own, companionScope),
    direct: thisContact?.outPath != null,
    companionScope,
  };
}
