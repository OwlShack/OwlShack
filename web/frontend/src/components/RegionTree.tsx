import { CornerDownRight } from "lucide-react";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

// A region and the one it sits under, "*" at the top as on firmware. Nesting only organises a list: a repeater matches the exact region, never its parent.
export interface RegionNode {
  name: string;
  parent: string;
}

export const NESTING_HINT = "Putting a region under another only organises the list: a repeater carries a region by its own name, so sending in nz does not reach a repeater that only carries akl.";

// regionTree walks the list from the top: each row with its depth and every place it could move to (not itself or below it). "*" is left out, and a region whose parent isn't listed shows at the top.
export function regionTree<T extends RegionNode>(regions: T[]) {
  const listed = new Set(regions.map((r) => r.name));
  const rows: { region: T; depth: number; under: string[] }[] = [];
  const walk = (level: T[], depth: number) => {
    for (const r of level) {
      rows.push({ region: r, depth, under: [] });
      walk(regions.filter((c) => c.parent === r.name && c.name !== "*"), depth + 1);
    }
  };
  walk(regions.filter((r) => r.name !== "*" && (r.parent === "*" || !listed.has(r.parent))), 0);
  rows.forEach((row, i) => {
    const below = new Set<string>();
    for (let j = i + 1; j < rows.length && rows[j].depth > row.depth; j++) below.add(rows[j].region.name);
    row.under = ["*", ...rows.map((r) => r.region.name).filter((n) => n !== row.region.name && !below.has(n))];
  });
  return rows;
}

export const underLabel = (parent: string) => (parent === "*" ? "top level" : parent);

const utf8 = new TextEncoder();

// regionNameError is the server's rule: the firmware's name bytes, at most 30. A repeater also takes "#" and "$" names, as the firmware does; the Settings list never.
export function regionNameError(name: string, repeater = false): string | null {
  if (name === "*") return "* is traffic with no region, not a name";
  const bytes = utf8.encode(name);
  if (bytes.length > 30) return "at most 30 bytes";
  for (const b of bytes) {
    if (b === 0x2d || (b >= 0x30 && b <= 0x39) || b >= 0x41 || (repeater && (b === 0x23 || b === 0x24))) continue;
    return b === 0x20 ? "no spaces" : `can't contain ${String.fromCharCode(b)}`;
  }
  return null;
}

// RegionName indents a region by its depth, with an arrow under its parent.
export function RegionName({ name, depth }: { name: string; depth: number }) {
  return (
    <span className="flex min-w-0 items-center gap-1.5 font-mono text-sm" style={{ paddingLeft: `${Math.max(depth - 1, 0) * 1.25}rem` }}>
      {depth > 0 && <CornerDownRight aria-hidden className="size-3 shrink-0 text-muted-foreground" />}
      <span className="truncate">{name}</span>
    </span>
  );
}

// UnderSelect moves a region: "*" reads as top level.
export function UnderSelect({
  name,
  value,
  options,
  onChange,
  disabled,
}: {
  name: string;
  value: string;
  options: string[];
  onChange: (parent: string) => void;
  disabled?: boolean;
}) {
  return (
    <Select value={value} disabled={disabled} onValueChange={onChange}>
      <SelectTrigger
        size="sm"
        aria-label={`put ${name} under`}
        className="w-36 gap-1.5 rounded-none border-border bg-background px-2 font-mono text-[11px] *:data-[slot=select-value]:flex-1"
      >
        <span className="text-muted-foreground">under</span>
        <SelectValue />
      </SelectTrigger>
      <SelectContent className="rounded-sm">
        {options.map((p) => (
          <SelectItem key={p} value={p} className="font-mono text-sm">
            {underLabel(p)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
