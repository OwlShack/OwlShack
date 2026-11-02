import { useMemo, useState, type ReactNode } from "react";
import { CircleDashed, Plus, Search, Trash2 } from "lucide-react";
import { PeerAvatar } from "@/components/PeerAvatar";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { truncateMid } from "@/lib/format";

export interface PickablePeer {
  pubkey: string;
  name: string;
  type?: string;
}

const HEX64_RE = /^[0-9a-fA-F]{64}$/;

// Only a companion originates a login or a plain DM; a repeater, room server or sensor never does.
export function isCompanionPeer(p: PickablePeer): boolean {
  return ["CHAT", ""].includes((p.type ?? "").toUpperCase());
}

export function isRepeaterPeer(p: PickablePeer): boolean {
  return (p.type ?? "").toUpperCase() === "REPEATER";
}

// PeerPicker is the searchable peer list (companions unless `accept` says otherwise) plus a
// manual-pubkey escape hatch, shared by every "choose a peer" surface so they filter and validate the same way.
export function PeerPicker({
  peers,
  knownPrefixes,
  onPick,
  disabled,
  manualInputId,
  alreadyAddedLabel = "already added",
  accept = isCompanionPeer,
}: {
  peers: PickablePeer[];
  knownPrefixes: Set<string>;
  onPick: (pubkey: string) => void | Promise<void>;
  disabled?: boolean;
  manualInputId: string;
  alreadyAddedLabel?: string;
  accept?: (p: PickablePeer) => boolean;
}) {
  const [search, setSearch] = useState("");
  const [manualKey, setManualKey] = useState("");

  const candidates = useMemo(() => {
    const pool = peers.filter(
      (p) => !knownPrefixes.has(p.pubkey.toLowerCase().slice(0, 12)) && accept(p),
    );
    const q = search.trim().toLowerCase();
    const filtered = q
      ? pool.filter(
          (p) =>
            p.name.toLowerCase().includes(q) || p.pubkey.toLowerCase().includes(q),
        )
      : pool;
    return [...filtered].sort((a, b) => (a.name || "").localeCompare(b.name || ""));
  }, [peers, knownPrefixes, search, accept]);

  const manualValid = HEX64_RE.test(manualKey.trim());
  const manualAlreadyAdded =
    manualValid && knownPrefixes.has(manualKey.trim().toLowerCase().slice(0, 12));

  return (
    <>
      <div className="space-y-2">
        <span className="label-overline block">Available peers</span>
        <div className="relative">
          <Search className="size-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground/60 pointer-events-none" />
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="search name or pubkey…"
            className="pl-8 rounded-none font-mono text-base md:text-xs h-8"
          />
        </div>
        <div className="border border-border max-h-64 overflow-y-auto divide-y divide-border">
          {candidates.length === 0 ? (
            <div className="px-4 py-8 text-center">
              <CircleDashed className="size-5 mx-auto mb-2 text-muted-foreground/40" />
              <p className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
                No matching peers
              </p>
            </div>
          ) : (
            candidates.map((p) => (
              <div
                key={p.pubkey}
                className="flex items-center gap-3 px-3 py-2 hover:bg-muted/40 transition-colors"
              >
                <PeerAvatar name={p.name || p.pubkey} size="sm" />
                <div className="min-w-0 flex-1 space-y-0.5">
                  <div className="flex items-center gap-2">
                    <span className="text-xs font-medium truncate">
                      {p.name || (
                        <span className="text-muted-foreground italic">unknown</span>
                      )}
                    </span>
                  </div>
                  <code className="font-mono text-[10px] text-muted-foreground">
                    {truncateMid(p.pubkey, 6, 4)}
                  </code>
                </div>
                <Button
                  variant="ghost"
                  size="xs"
                  disabled={disabled}
                  onClick={() => onPick(p.pubkey.toLowerCase())}
                  className="font-mono uppercase tracking-widest text-primary hover:text-primary"
                >
                  <Plus className="size-3" /> add
                </Button>
              </div>
            ))
          )}
        </div>
      </div>

      <div className="border-t border-border pt-4 space-y-2">
        <Label
          htmlFor={manualInputId}
          className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground"
        >
          Manual pubkey
        </Label>
        <div className="flex gap-2">
          <Input
            id={manualInputId}
            value={manualKey}
            onChange={(e) => setManualKey(e.target.value)}
            placeholder="64-character hex…"
            spellCheck={false}
            autoCorrect="off"
            autoCapitalize="off"
            aria-invalid={manualKey.length > 0 && (!manualValid || manualAlreadyAdded)}
            className="rounded-none font-mono text-base md:text-xs h-8 flex-1"
          />
          <Button
            variant="default"
            size="sm"
            onClick={() => {
              onPick(manualKey.trim().toLowerCase());
              setManualKey("");
            }}
            disabled={!manualValid || manualAlreadyAdded || disabled}
            className="font-mono uppercase tracking-widest"
          >
            <Plus className="size-3" /> add
          </Button>
        </div>
        {manualKey.length > 0 && !manualValid && (
          <p className="text-[10px] text-destructive font-mono">
            must be 64 hex characters
          </p>
        )}
        {manualAlreadyAdded && (
          <p className="text-[10px] text-destructive font-mono">{alreadyAddedLabel}</p>
        )}
      </div>
    </>
  );
}

// PeerListField is a chosen-peer list with an add dialog, for any config field holding pubkeys.
// Entries are stored as pubkeys; a legacy entry that is not hex renders as its raw text.
export function PeerListField({
  label,
  values,
  onChange,
  peers,
  addLabel,
  emptyHint,
  hint,
  dialogTitle,
  dialogDescription,
  idPrefix,
  accept,
  rowExtra,
}: {
  label: string;
  values: string[];
  onChange: (next: string[]) => void;
  peers: PickablePeer[];
  addLabel: string;
  emptyHint: string;
  hint?: ReactNode;
  dialogTitle: string;
  dialogDescription: string;
  idPrefix: string;
  accept?: (p: PickablePeer) => boolean;
  // rowExtra renders a per-entry control beside the remove button.
  rowExtra?: (value: string) => ReactNode;
}) {
  const [open, setOpen] = useState(false);

  const byPrefix = useMemo(() => {
    const m = new Map<string, PickablePeer>();
    for (const p of peers) m.set(p.pubkey.toLowerCase().slice(0, 12), p);
    return m;
  }, [peers]);

  const chosenPrefixes = useMemo(
    () => new Set(values.map((v) => v.trim().toLowerCase().slice(0, 12))),
    [values],
  );

  return (
    <div className="space-y-1">
      <Label className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
        {label}
      </Label>
      <div className="space-y-2">
        {values.length === 0 && (
          <p className="font-mono text-xs text-muted-foreground/50">{emptyHint}</p>
        )}
        {values.map((value, i) => {
          const peer = byPrefix.get(value.trim().toLowerCase().slice(0, 12));
          const isKey = /^[0-9a-fA-F]{2,64}$/.test(value.trim());
          return (
            <div
              key={`${value}-${i}`}
              className="flex items-center gap-3 border border-border px-3 py-2"
            >
              <PeerAvatar name={peer?.name || value} size="sm" />
              <div className="min-w-0 flex-1 space-y-0.5">
                <span className="block truncate text-xs font-medium">
                  {peer?.name || (isKey ? <span className="text-muted-foreground italic">unknown peer</span> : value)}
                </span>
                {isKey && (
                  <code className="font-mono text-[10px] text-muted-foreground">
                    {truncateMid(value.trim(), 6, 4)}
                  </code>
                )}
              </div>
              {rowExtra?.(value)}
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                onClick={() => onChange(values.filter((_, j) => j !== i))}
                aria-label={`Remove ${peer?.name || value}`}
                className="shrink-0 rounded-none text-muted-foreground/60 hover:text-destructive"
              >
                <Trash2 className="size-3.5" />
              </Button>
            </div>
          );
        })}
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => setOpen(true)}
          className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
        >
          <Plus className="size-3.5" />
          {addLabel}
        </Button>
      </div>
      {hint && <p className="font-mono text-[10px] text-muted-foreground/60">{hint}</p>}

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="rounded-none border-border bg-card max-w-2xl">
          <DialogHeader>
            <DialogTitle className="font-mono uppercase tracking-[0.08em] text-sm">
              {dialogTitle}
            </DialogTitle>
            <DialogDescription className="text-xs text-muted-foreground">
              {dialogDescription}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <PeerPicker
              peers={peers}
              knownPrefixes={chosenPrefixes}
              manualInputId={`${idPrefix}-manual-pubkey`}
              alreadyAddedLabel="already on the list"
              accept={accept}
              onPick={(pubkey) => {
                onChange([...values, pubkey]);
                setOpen(false);
              }}
            />
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
