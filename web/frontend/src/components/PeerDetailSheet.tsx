import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import QRCode from "qrcode";
import {
  Check,
  Copy,
  KeyRound,
  Link2,
  MapPin,
  MessageSquarePlus,
  QrCode,
  Radio,
  Route,
  Share2,
  User,
  UserPlus,
  X,
} from "lucide-react";
import { toast } from "sonner";
import { HopPath, type PathPeer } from "@/components/HopPath";
import { mapPathHref } from "@/lib/linkPath";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetTitle,
} from "@/components/ui/sheet";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { PeerAvatar } from "@/components/PeerAvatar";
import { PeerTypePill } from "@/components/StatusIndicator";
import { SignalStrength } from "@/components/SignalStrength";
import { InlineConfirm } from "@/components/InlineConfirm";
import { formatDateTime, timeAgo, truncateMid } from "@/lib/format";
import { contactDetailPath } from "@/lib/routes";
import { deletePeer } from "@/lib/peerApi";
import { companionPath, companionRef } from "@/lib/companionRef";

export interface PeerLike {
  pubkey: string;
  name: string;
  type: string;
  lat: number;
  lon: number;
  snr: number | null;
  rssi: number | null;
  lastSeen: string;
  lastAdvertTs?: number;
  outPath?: string;
  outPathHashSize?: number;
}

interface CompanionRef {
  id: number;
  name: string;
  pubkey?: string;
}

const TYPE_INT: Record<string, number> = {
  CHAT: 1,
  REPEATER: 2,
  ROOM: 3,
  SENSOR: 4,
};
const CONTACT_TYPES = ["CHAT", "REPEATER", "ROOM", "SENSOR"];

function typeToInt(t: string): number {
  return TYPE_INT[t] ?? 1;
}

// hashSize 0 means the default 1 byte per hop.
export function advertPathInfo(outPath?: string, hashSize?: number) {
  const hex = (outPath || "").toLowerCase();
  const hs = hashSize && hashSize > 0 ? hashSize : 1;
  const chunk = hs * 2;
  const path: string[] = [];
  for (let i = 0; i + chunk <= hex.length; i += chunk) {
    path.push(hex.slice(i, i + chunk));
  }
  return { hops: path.length, hashSize: hs, path };
}

function shareLink(peer: PeerLike): string {
  const name = encodeURIComponent(peer.name || "");
  return `meshcore://contact/add?name=${name}&public_key=${peer.pubkey.toLowerCase()}&type=${typeToInt(peer.type)}`;
}

function contactEmbed(peer: PeerLike): string {
  return `<${peer.pubkey.toLowerCase()}:${typeToInt(peer.type)}:${peer.name}>`;
}

async function copy(text: string, label: string) {
  try {
    await navigator.clipboard.writeText(text);
    toast.success(`${label} copied`);
  } catch {
    toast.error("Clipboard unavailable");
  }
}

export function PeerDetailSheet({
  peer,
  peers,
  open,
  onOpenChange,
  companions,
}: {
  peer: PeerLike | null;
  peers: PathPeer[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  companions: CompanionRef[];
}) {
  const [addOpen, setAddOpen] = useState(false);
  const [qrOpen, setQrOpen] = useState(false);
  const [shareMsgOpen, setShareMsgOpen] = useState(false);
  // Adding a contact changes neither the peer nor the companion list, so nothing the membership
  // fetch depends on moves and it would keep showing the peer as unattached until a reload.
  const [membershipVersion, setMembershipVersion] = useState(0);

  return (
    <>
      <Sheet open={open} onOpenChange={onOpenChange}>
        <SheetContent
          side="right"
          showCloseButton={false}
          className="w-full max-w-[100vw] border-l border-border bg-card p-0 sm:max-w-md"
        >
          <SheetTitle className="sr-only">Peer detail</SheetTitle>
          <SheetDescription className="sr-only">
            Details and actions for the selected discovered peer.
          </SheetDescription>
          {peer && (
            <PeerDetailBody
              peer={peer}
              peers={peers}
              companions={companions}
              membershipVersion={membershipVersion}
              onClose={() => onOpenChange(false)}
              onAdd={() => setAddOpen(true)}
              onShareInMessage={() => setShareMsgOpen(true)}
              onShowQr={() => setQrOpen(true)}
            />
          )}
        </SheetContent>
      </Sheet>

      {peer && (
        <>
          <AddPeerContactDialog
            peer={peer}
            companions={companions}
            open={addOpen}
            onOpenChange={setAddOpen}
            onAdded={() => setMembershipVersion((v) => v + 1)}
          />
          <QrDialog peer={peer} open={qrOpen} onOpenChange={setQrOpen} />
          <ShareInMessageDialog
            peer={peer}
            companions={companions}
            open={shareMsgOpen}
            onOpenChange={setShareMsgOpen}
          />
        </>
      )}
    </>
  );
}

function PeerDetailBody({
  peer,
  peers,
  companions,
  membershipVersion,
  onClose,
  onAdd,
  onShareInMessage,
  onShowQr,
}: {
  peer: PeerLike;
  peers: PathPeer[];
  companions: CompanionRef[];
  membershipVersion: number;
  onClose: () => void;
  onAdd: () => void;
  onShareInMessage: () => void;
  onShowQr: () => void;
}) {
  const navigate = useNavigate();
  const path = useMemo(
    () => advertPathInfo(peer.outPath, peer.outPathHashSize),
    [peer.outPath, peer.outPathHashSize],
  );
  // Our own companion identities surface as discovered peers; a node can't be its own contact.
  const isSelf = useMemo(
    () =>
      companions.some(
        (c) => c.pubkey?.toLowerCase() === peer.pubkey.toLowerCase(),
      ),
    [companions, peer.pubkey],
  );
  const membership = usePeerMembership(peer, companions, membershipVersion);
  const lat = peer.lat / 1e6;
  const lon = peer.lon / 1e6;
  const hasLocation = peer.lat !== 0 || peer.lon !== 0;
  const coords = `${lat.toFixed(6)}, ${lon.toFixed(6)}`;
  // "View on map" is pointless when the sheet was opened from the map itself.
  const onMapPage = useLocation().pathname === "/map";
  const showMapLink = hasLocation && !onMapPage;

  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const handleDelete = async () => {
    setDeleting(true);
    try {
      await deletePeer(peer.pubkey);
      toast.success("Peer deleted");
      onClose(); // the WS "delete" broadcast drops it from the live lists
    } catch (e) {
      toast.error(
        `Failed to delete: ${e instanceof Error ? e.message : "error"}`,
      );
      setDeleting(false);
      setConfirmDelete(false);
    }
  };

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* Header */}
      <div className="flex items-start justify-between border-b border-border px-5 py-4">
        <span className="label-overline">Discovered peer</span>
        <button
          type="button"
          onClick={onClose}
          className="p-3 -m-3 text-muted-foreground hover:text-foreground"
          aria-label="Close"
        >
          <X className="size-4" />
        </button>
      </div>

      <div className="flex-1 overflow-y-auto">
        {/* Identity + actions */}
        <div className="flex flex-col items-center gap-3 border-b border-border px-5 py-6">
          <PeerAvatar name={peer.name || peer.pubkey} size="lg" />
          <div className="space-y-1 text-center">
            <div className="flex items-center justify-center gap-2">
              <span className="text-base font-semibold">
                {peer.name || (
                  <span className="italic text-muted-foreground">unknown</span>
                )}
              </span>
              <PeerTypePill type={peer.type} />
            </div>
            <code className="font-mono text-[11px] text-muted-foreground">
              &lt;{truncateMid(peer.pubkey, 6, 6)}&gt;
            </code>
          </div>
          <div className="flex items-center gap-2 pt-1">
            {isSelf ? (
              <span className="inline-flex items-center gap-1.5 border border-primary/40 bg-primary/10 px-2.5 py-1 font-mono text-xs uppercase tracking-widest text-primary">
                <Radio className="size-3.5" /> Your node
              </span>
            ) : (
              <Button
                size="sm"
                onClick={onAdd}
                className="font-mono text-xs uppercase tracking-widest"
              >
                <UserPlus className="size-3.5" /> Add
              </Button>
            )}
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  size="sm"
                  variant="outline"
                  className="rounded-none font-mono text-xs uppercase tracking-widest"
                >
                  <Share2 className="size-3.5" /> Share
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent
                align="center"
                className="rounded-none font-mono text-xs"
              >
                <DropdownMenuItem
                  onClick={() => copy(peer.pubkey.toLowerCase(), "Public key")}
                >
                  <KeyRound className="size-3.5" /> Copy public key
                </DropdownMenuItem>
                <DropdownMenuItem
                  onClick={() => copy(shareLink(peer), "Sharing link")}
                >
                  <Link2 className="size-3.5" /> Copy sharing link
                </DropdownMenuItem>
                <DropdownMenuItem onClick={onShowQr}>
                  <QrCode className="size-3.5" /> Show QR code
                </DropdownMenuItem>
                <DropdownMenuItem onClick={onShareInMessage}>
                  <MessageSquarePlus className="size-3.5" /> Share in a message
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </div>

        {/* Companions this peer is already a contact of */}
        <CompanionMembership
          pubkeyHex={peer.pubkey.toLowerCase()}
          hits={membership}
        />

        {/* Fields */}
        <div className="divide-y divide-border/60">
          <DetailRow icon={<KeyRound className="size-4" />} label="Public Key">
            <div className="flex items-start justify-between gap-2">
              <code className="break-all font-mono text-xs text-foreground">
                {peer.pubkey.toLowerCase()}
              </code>
              <CopyButton text={peer.pubkey.toLowerCase()} label="Public key" />
            </div>
          </DetailRow>

          {hasLocation && (
            <DetailRow icon={<MapPin className="size-4" />} label="Position">
              <div className="flex items-center justify-between gap-2">
                <code className="font-mono text-xs tabular-nums text-foreground">
                  {coords}
                </code>
                <div className="flex items-center gap-1">
                  {showMapLink && (
                    <button
                      type="button"
                      onClick={() => navigate(`/map?lat=${lat}&lon=${lon}`)}
                      className="text-muted-foreground hover:text-primary"
                      aria-label="View on map"
                    >
                      <MapPin className="size-3.5" />
                    </button>
                  )}
                  <CopyButton text={coords} label="Position" />
                </div>
              </div>
            </DetailRow>
          )}

          <DetailRow icon={<Radio className="size-4" />} label="Signal">
            <div className="flex flex-wrap items-center gap-x-4 gap-y-1 font-mono text-xs tabular-nums">
              <span className="inline-flex items-center gap-1.5">
                <SignalStrength snr={peer.snr} size="md" showLabel={false} />
                <span className="text-muted-foreground/70">SNR</span>
                <span>{peer.snr != null ? `${peer.snr.toFixed(1)} dB` : "—"}</span>
              </span>
              {peer.rssi != null && (
                <span>
                  <span className="text-muted-foreground/70">RSSI</span>{" "}
                  {peer.rssi} dBm
                </span>
              )}
            </div>
          </DetailRow>

          <DetailRow icon={<Radio className="size-4" />} label="Last Advert Heard">
            <div className="font-mono text-xs">
              <div className="text-foreground">{timeAgo(peer.lastSeen)}</div>
              <div className="text-muted-foreground/70">
                {formatDateTime(peer.lastSeen)}
              </div>
            </div>
          </DetailRow>

          <DetailRow icon={<Route className="size-4" />} label="Advert Path">
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <span className="font-mono text-[10px] uppercase tracking-widest text-muted-foreground">
                  Hops away
                </span>
                <span className="font-mono text-xs tabular-nums">
                  {path.hops === 0 ? "direct (0 hops)" : `${path.hops} hops`}
                </span>
              </div>
              {path.hops > 0 && (
                <>
                  <HopPath
                    path={peer.outPath}
                    hashSize={peer.outPathHashSize}
                    direction="rx"
                    originName={peer.name}
                    peers={peers}
                  />
                  <Link
                    to={mapPathHref({
                      path: peer.outPath ?? "",
                      hashSize: peer.outPathHashSize,
                      origin: peer.pubkey,
                    })}
                    onClick={onClose}
                    className="inline-flex items-center gap-1 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground hover:text-primary"
                  >
                    <MapPin className="size-3" />
                    on map
                  </Link>
                </>
              )}
              <div className="flex items-center justify-between">
                <span className="font-mono text-[10px] uppercase tracking-widest text-muted-foreground">
                  Hash size
                </span>
                <span className="font-mono text-xs tabular-nums text-muted-foreground">
                  {path.hashSize}-byte{path.hashSize > 1 ? "s" : ""} per hop
                </span>
              </div>
            </div>
          </DetailRow>
        </div>

        {/* Extra tools */}
        {showMapLink && (
          <div className="border-t border-border px-5 py-4">
            <span className="label-overline">Extra tools</span>
            <button
              type="button"
              onClick={() => navigate(`/map?lat=${lat}&lon=${lon}`)}
              className="mt-2 flex w-full items-center justify-between border border-border px-3 py-2.5 text-left transition-colors hover:border-primary/40 hover:bg-muted/40"
            >
              <span className="flex items-center gap-2 font-mono text-xs uppercase tracking-[0.08em]">
                <MapPin className="size-3.5 text-primary" /> View on map
              </span>
              <span className="text-muted-foreground/50">›</span>
            </button>
          </div>
        )}

        {/* Danger zone — a saved contact keeps its own cached identity, so this never touches contacts */}
        <div className="flex items-center justify-between border-t border-border px-5 py-4">
          <div className="space-y-0.5">
            <span className="label-overline">Discovered peer</span>
            <p className="text-[11px] text-muted-foreground/70">
              Removes it everywhere{deleting ? "…" : ""}; re-adds itself if heard
              again.
            </p>
          </div>
          {deleting ? (
            <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
              deleting…
            </span>
          ) : (
            <InlineConfirm
              confirming={confirmDelete}
              onAskRemove={() => setConfirmDelete(true)}
              onCancel={() => setConfirmDelete(false)}
              onConfirm={handleDelete}
              triggerLabel="delete"
            />
          )}
        </div>
      </div>
    </div>
  );
}

interface MembershipHit {
  companion: string;
  ref: string;
  isRepeater: boolean;
  peerType: string;
}

// One fetch shared by the "In companions" list and the delete gate.
function usePeerMembership(
  peer: PeerLike,
  companions: CompanionRef[],
  version: number,
): MembershipHit[] {
  const [hits, setHits] = useState<MembershipHit[]>([]);
  const key = peer.pubkey.toLowerCase();
  // `companions` is live WS data — depend on a stable name key so revalidation can't re-fire the fan-out.
  const names = companions.map((c) => c.name).join(",");
  const companionsRef = useRef(companions);
  companionsRef.current = companions;

  useEffect(() => {
    let cancelled = false;
    setHits([]);
    Promise.all(
      companionsRef.current.map((c) =>
        fetch(`/api/companions/${encodeURIComponent(c.name)}/contacts/${key}`)
          .then((r) => (r.ok ? r.json() : null))
          .then((contact) =>
            contact
              ? ({
                  companion: c.name,
                  ref: companionRef(c),
                  isRepeater:
                    contact.metadata?.isRepeater === true ||
                    (contact.type || "").toUpperCase() === "REPEATER",
                  peerType: contact.type || (contact.metadata?.isRepeater ? "REPEATER" : ""),
                } as MembershipHit)
              : null,
          )
          .catch(() => null),
      ),
    ).then((res) => {
      if (!cancelled) setHits(res.filter((h): h is MembershipHit => h !== null));
    });
    return () => {
      cancelled = true;
    };
  }, [key, names, version]);

  return hits;
}

// Each hit links to where the contact lives: repeater admin page or contact detail.
function CompanionMembership({
  pubkeyHex,
  hits,
}: {
  pubkeyHex: string;
  hits: MembershipHit[];
}) {
  const navigate = useNavigate();

  if (hits.length === 0) return null;

  return (
    <div className="border-b border-border px-5 py-4">
      <span className="label-overline">In companions</span>
      <div className="mt-2 space-y-1.5">
        {hits.map((h) => {
          const to = contactDetailPath(h.ref, pubkeyHex, h.peerType);
          return (
            <button
              key={h.companion}
              type="button"
              onClick={() => navigate(to)}
              className="flex w-full items-center justify-between border border-border px-3 py-2.5 text-left transition-colors hover:border-primary/40 hover:bg-muted/40"
            >
              <span className="flex items-center gap-2 font-mono text-xs uppercase tracking-[0.08em]">
                {h.isRepeater ? (
                  <Radio className="size-3.5 text-primary" />
                ) : (
                  <User className="size-3.5 text-primary" />
                )}
                {h.companion}
                <span className="text-muted-foreground/60">
                  {h.isRepeater ? "· repeater" : "· contact"}
                </span>
              </span>
              <span className="text-muted-foreground/50">›</span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

function DetailRow({
  icon,
  label,
  children,
}: {
  icon: React.ReactNode;
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex gap-3 px-5 py-3.5">
      <div className="mt-0.5 shrink-0 text-muted-foreground/60">{icon}</div>
      <div className="min-w-0 flex-1 space-y-1">
        <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/70">
          {label}
        </span>
        {children}
      </div>
    </div>
  );
}

function CopyButton({ text, label }: { text: string; label: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      onClick={() => {
        copy(text, label);
        setDone(true);
        window.setTimeout(() => setDone(false), 1200);
      }}
      className="shrink-0 p-3 -m-3 text-muted-foreground hover:text-primary"
      aria-label={`Copy ${label}`}
    >
      {done ? (
        <Check className="size-3.5 text-success" />
      ) : (
        <Copy className="size-3.5" />
      )}
    </button>
  );
}

function AddPeerContactDialog({
  peer,
  companions,
  open,
  onOpenChange,
  onAdded,
}: {
  peer: PeerLike;
  companions: CompanionRef[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onAdded: () => void;
}) {
  const [companion, setCompanion] = useState("");
  const [submitting, setSubmitting] = useState(false);

  // Reset only on closed→open: `companions` is live WS data and must not wipe the selection.
  const prevOpenRef = useRef(false);
  useEffect(() => {
    if (open && !prevOpenRef.current) {
      setCompanion(companions[0]?.name ?? "");
      setSubmitting(false);
    }
    prevOpenRef.current = open;
  }, [open, companions]);

  const submit = async () => {
    if (!companion) return;
    setSubmitting(true);
    try {
      const type = CONTACT_TYPES.includes(peer.type) ? peer.type : "";
      const res = await fetch(
        `/api/companions/${encodeURIComponent(companion)}/contacts`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            pubkey: peer.pubkey.toLowerCase(),
            name: peer.name,
            type,
          }),
        },
      );
      if (!res.ok) {
        const e = await res.json().catch(() => ({}));
        throw new Error(e.error || `HTTP ${res.status}`);
      }
      toast.success(`Added to ${companion}'s contacts`);
      onAdded();
      onOpenChange(false);
    } catch (e) {
      toast.error(
        `Failed to add contact: ${e instanceof Error ? e.message : "error"}`,
      );
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md rounded-none border-border bg-card">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.08em]">
            Add to contacts
          </DialogTitle>
          <DialogDescription className="text-xs text-muted-foreground">
            Authorize this peer for direct messaging with a companion.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="flex items-center gap-3 border border-border bg-background/60 p-2.5">
            <PeerAvatar name={peer.name || peer.pubkey} size="sm" />
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-1.5">
                <span className="truncate text-sm font-medium">
                  {peer.name || "unknown"}
                </span>
                <PeerTypePill type={peer.type} />
              </div>
              <code className="block truncate font-mono text-[10px] text-muted-foreground">
                {truncateMid(peer.pubkey, 8, 6)}
              </code>
            </div>
          </div>

          <div className="space-y-1.5">
            <span className="label-overline">Companion</span>
            <Select value={companion} onValueChange={setCompanion}>
              <SelectTrigger className="w-full rounded-none font-mono text-xs uppercase tracking-[0.06em]">
                <SelectValue placeholder="Select companion" />
              </SelectTrigger>
              <SelectContent className="rounded-none font-mono text-xs">
                {companions.length === 0 ? (
                  <div className="px-3 py-2 text-muted-foreground/60">
                    none configured
                  </div>
                ) : (
                  companions.map((c) => (
                    <SelectItem
                      key={c.name}
                      value={c.name}
                      className="font-mono text-xs uppercase tracking-[0.06em]"
                    >
                      {c.name}
                    </SelectItem>
                  ))
                )}
              </SelectContent>
            </Select>
          </div>

          <div className="flex justify-end gap-2 pt-1">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => onOpenChange(false)}
              className="font-mono uppercase tracking-widest"
            >
              Cancel
            </Button>
            <Button
              size="sm"
              onClick={submit}
              disabled={!companion || submitting}
              className="font-mono uppercase tracking-widest"
            >
              <UserPlus className="size-3.5" />
              {submitting ? "Adding…" : "Add"}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function QrDialog({
  peer,
  open,
  onOpenChange,
}: {
  peer: PeerLike;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const link = useMemo(() => shareLink(peer), [peer]);
  const [dataUrl, setDataUrl] = useState("");

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    QRCode.toDataURL(link, { margin: 2, width: 320 })
      .then((url) => {
        if (!cancelled) setDataUrl(url);
      })
      .catch(() => {
        if (!cancelled) setDataUrl("");
      });
    return () => {
      cancelled = true;
    };
  }, [open, link]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xs rounded-none border-border bg-card">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.08em]">
            Contact QR
          </DialogTitle>
          <DialogDescription className="text-xs text-muted-foreground">
            Scan with the MeshCore app to add {peer.name || "this peer"}.
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col items-center gap-3">
          {dataUrl ? (
            <img
              src={dataUrl}
              alt="Contact QR code"
              className="size-56 border border-border bg-white"
            />
          ) : (
            <div className="flex size-56 items-center justify-center border border-border text-xs text-muted-foreground">
              generating…
            </div>
          )}
          <div className="flex w-full items-center justify-between gap-2 border border-border bg-background/60 px-2 py-1.5">
            <code className="truncate font-mono text-[10px] text-muted-foreground">
              {link}
            </code>
            <CopyButton text={link} label="Sharing link" />
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

interface Conversation {
  id: string;
  type: string;
  name: string;
  channel: string;
  isRepeater?: boolean;
}

function ShareInMessageDialog({
  peer,
  companions,
  open,
  onOpenChange,
}: {
  peer: PeerLike;
  companions: CompanionRef[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const navigate = useNavigate();
  const [companion, setCompanion] = useState("");
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [channel, setChannel] = useState("");
  const [loadingConv, setLoadingConv] = useState(false);

  useEffect(() => {
    if (open) setCompanion(companions[0]?.name ?? "");
  }, [open, companions]);

  useEffect(() => {
    if (!open || !companion) {
      setConversations([]);
      setChannel("");
      return;
    }
    let cancelled = false;
    setLoadingConv(true);
    fetch(`/api/companions/${encodeURIComponent(companion)}/conversations`)
      .then((r) => (r.ok ? r.json() : []))
      .then((cs: Conversation[]) => {
        if (cancelled) return;
        // Repeaters aren't DM targets — they're managed on the Repeaters page.
        const list = (cs || []).filter((c) => !c.isRepeater);
        setConversations(list);
        setChannel(list[0]?.channel ?? "");
      })
      .catch(() => !cancelled && setConversations([]))
      .finally(() => !cancelled && setLoadingConv(false));
    return () => {
      cancelled = true;
    };
  }, [open, companion]);

  const embed = contactEmbed(peer);

  const go = () => {
    if (!companion || !channel) return;
    onOpenChange(false);
    const target = companions.find((c) => c.name === companion);
    navigate(
      `${target ? companionPath(target) : `/companions/${encodeURIComponent(companion)}`}?channel=${encodeURIComponent(channel)}&compose=${encodeURIComponent(embed)}`,
    );
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md rounded-none border-border bg-card">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.08em]">
            Share in a message
          </DialogTitle>
          <DialogDescription className="text-xs text-muted-foreground">
            Opens the chat with the contact pre-filled — you review and send.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1.5">
            <span className="label-overline">Companion</span>
            <Select value={companion} onValueChange={setCompanion}>
              <SelectTrigger className="w-full rounded-none font-mono text-xs uppercase tracking-[0.06em]">
                <SelectValue placeholder="Select companion" />
              </SelectTrigger>
              <SelectContent className="rounded-none font-mono text-xs">
                {companions.map((c) => (
                  <SelectItem
                    key={c.name}
                    value={c.name}
                    className="font-mono text-xs uppercase tracking-[0.06em]"
                  >
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-1.5">
            <span className="label-overline">Conversation</span>
            <Select
              value={channel}
              onValueChange={setChannel}
              disabled={loadingConv || conversations.length === 0}
            >
              <SelectTrigger className="w-full rounded-none font-mono text-xs">
                <SelectValue
                  placeholder={
                    loadingConv ? "loading…" : "no conversations"
                  }
                />
              </SelectTrigger>
              <SelectContent className="rounded-none font-mono text-xs">
                {conversations.map((c) => (
                  <SelectItem key={c.id} value={c.channel}>
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-1">
            <span className="label-overline">Message preview</span>
            <code className="block break-all border border-border bg-background/60 px-2 py-1.5 font-mono text-[11px] text-muted-foreground">
              {embed}
            </code>
          </div>

          <div className="flex justify-end gap-2 pt-1">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => onOpenChange(false)}
              className="font-mono uppercase tracking-widest"
            >
              Cancel
            </Button>
            <Button
              size="sm"
              onClick={go}
              disabled={!companion || !channel}
              className="font-mono uppercase tracking-widest"
            >
              <MessageSquarePlus className="size-3.5" /> Open in composer
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
