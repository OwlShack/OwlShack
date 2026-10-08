import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  Ban,
  ClipboardCopy,
  DoorOpen,
  Info,
  MapPin,
  MoreVertical,
  Pencil,
  Route,
  Search,
  Share2,
  Trash2,
  Users,
  X,
} from "lucide-react";
import QRCode from "qrcode";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { PathDialog } from "@/components/PathDialog";
import { ChannelShare } from "@/components/ChannelShare";
import { RegionSelect, regionName, useCompanionScope, useRegionSettings } from "@/components/RegionSelect";
import { useApiList } from "@/hooks/useApiList";
import { useApiObject } from "@/hooks/useApiObject";
import { configApi, request, type ConfigChannel, type FloodScope } from "@/lib/configApi";
import { contactDetailPath } from "@/lib/routes";
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
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

interface Conversation {
  id: string;
  type: string;
  name: string;
  channel: string;
  pubkey?: string;
  peerType?: string;
}

interface ChatHeaderMenuProps {
  companion: string;
  companionId: number | null;
  conversation: Conversation;
  // sendScope is the region this thread's sends go in, which a shared channel carries.
  sendScope: FloodScope;
  onSearchToggle: () => void;
  onMessagesCleared: () => void;
  onRegionChanged: () => void;
}

type DialogKind =
  | "share"
  | "region"
  | "rename"
  | "participants"
  | "blocked"
  | "deleteHistory"
  | "path"
  | null;

export function ChatHeaderMenu({
  companion,
  companionId,
  conversation,
  sendScope,
  onSearchToggle,
  onMessagesCleared,
  onRegionChanged,
}: ChatHeaderMenuProps) {
  const [dialog, setDialog] = useState<DialogKind>(null);
  const navigate = useNavigate();

  const isPublicChannel =
    conversation.type === "channel" &&
    (conversation.name.toLowerCase() === "public" ||
      conversation.channel.toLowerCase() === "public");
  const isChannel = conversation.type === "channel";
  const isContact = conversation.type === "contact";
  const managed = conversation.pubkey && ["ROOM", "SENSOR"].includes(conversation.peerType?.toUpperCase() ?? "");

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            className="relative size-8 grid place-items-center text-muted-foreground hover:text-foreground hover:bg-muted/60 border border-transparent hover:border-border rounded-sm before:absolute before:-inset-1 before:content-[''] sm:before:hidden"
            aria-label="Chat options"
          >
            <MoreVertical className="size-4" strokeWidth={1.6} />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="rounded-none border-border min-w-44">
          <DropdownMenuItem
            onClick={onSearchToggle}
            className="font-mono text-xs uppercase tracking-[0.08em] rounded-none"
          >
            <Search className="size-3.5" />
            Search
          </DropdownMenuItem>

          <DropdownMenuItem
            onClick={() => setDialog("share")}
            className="font-mono text-xs uppercase tracking-[0.08em] rounded-none"
          >
            <Share2 className="size-3.5" />
            Share
          </DropdownMenuItem>

          <DropdownMenuItem
            onClick={() => setDialog("region")}
            className="font-mono text-xs uppercase tracking-[0.08em] rounded-none"
          >
            <MapPin className="size-3.5" />
            Region
          </DropdownMenuItem>

          {managed && (
            <DropdownMenuItem
              onClick={() => navigate(contactDetailPath(companion, conversation.pubkey!, conversation.peerType))}
              className="font-mono text-xs uppercase tracking-[0.08em] rounded-none"
            >
              <DoorOpen className="size-3.5" />
              Manage
            </DropdownMenuItem>
          )}

          {isPublicChannel && (
            <DropdownMenuItem
              onClick={() => setDialog("rename")}
              className="font-mono text-xs uppercase tracking-[0.08em] rounded-none"
            >
              <Pencil className="size-3.5" />
              Rename
            </DropdownMenuItem>
          )}

          {isChannel && (
            <>
              <DropdownMenuItem
                onClick={() => setDialog("participants")}
                className="font-mono text-xs uppercase tracking-[0.08em] rounded-none"
              >
                <Users className="size-3.5" />
                Participants
              </DropdownMenuItem>

              <DropdownMenuItem
                onClick={() => setDialog("blocked")}
                className="font-mono text-xs uppercase tracking-[0.08em] rounded-none"
              >
                <Ban className="size-3.5" />
                Blocked Senders
              </DropdownMenuItem>
            </>
          )}

          {isContact && (
            <>
              {conversation.pubkey && (
                <DropdownMenuItem
                  onClick={() =>
                    navigate(
                      `/companions/${encodeURIComponent(companion)}/contacts/${conversation.pubkey}`,
                    )
                  }
                  className="font-mono text-xs uppercase tracking-[0.08em] rounded-none"
                >
                  <Info className="size-3.5" />
                  Details
                </DropdownMenuItem>
              )}
              {conversation.pubkey && (
                <DropdownMenuItem
                  onClick={() => setDialog("path")}
                  className="font-mono text-xs uppercase tracking-[0.08em] rounded-none"
                >
                  <Route className="size-3.5" />
                  Edit path
                </DropdownMenuItem>
              )}
            </>
          )}

          <DropdownMenuSeparator className="bg-border" />

          <DropdownMenuItem
            onClick={() => setDialog("deleteHistory")}
            className="font-mono text-xs uppercase tracking-[0.08em] rounded-none text-destructive focus:text-destructive"
          >
            <Trash2 className="size-3.5" />
            Delete Message History
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <ShareDialog
        open={dialog === "share"}
        onClose={() => setDialog(null)}
        companion={companion}
        conversation={conversation}
        region={regionName(sendScope)}
      />
      {dialog === "region" && (
        <ThreadRegionDialog
          onClose={() => setDialog(null)}
          companion={companion}
          companionId={companionId}
          conversation={conversation}
          onSaved={onRegionChanged}
        />
      )}
      <RenameDialog
        open={dialog === "rename"}
        onClose={() => setDialog(null)}
        companion={companion}
        conversation={conversation}
      />
      <ParticipantsDialog
        open={dialog === "participants"}
        onClose={() => setDialog(null)}
        companion={companion}
        conversation={conversation}
      />
      <BlockedSendersDialog
        open={dialog === "blocked"}
        onClose={() => setDialog(null)}
        companion={companion}
        conversation={conversation}
      />
      <DeleteHistoryDialog
        open={dialog === "deleteHistory"}
        onClose={() => setDialog(null)}
        companion={companion}
        conversation={conversation}
        onDeleted={onMessagesCleared}
      />
      {conversation.pubkey && (
        <PathDialog
          open={dialog === "path"}
          onOpenChange={(o) => !o && setDialog(null)}
          companion={companion}
          pubkey={conversation.pubkey}
          name={conversation.name}
        />
      )}
    </>
  );
}

// The MeshCore app's contact types: a shared link adds the node as the right kind.
const contactType: Record<string, number> = { CHAT: 1, REPEATER: 2, ROOM: 3, SENSOR: 4 };

function ShareDialog({
  open,
  onClose,
  companion,
  conversation,
  region,
}: {
  open: boolean;
  onClose: () => void;
  companion: string;
  conversation: Conversation;
  region: string | null;
}) {
  const [channelKey, setChannelKey] = useState<{ name: string; key: string } | null>(null);
  const [qrDataUrl, setQrDataUrl] = useState<string | null>(null);

  const isChannel = conversation.type === "channel";
  const isContact = conversation.type === "contact";
  const pubkey = conversation.pubkey || "";
  const type = contactType[conversation.peerType?.toUpperCase() ?? ""] ?? 1;

  useEffect(() => {
    if (!open || !isContact || !pubkey) {
      setQrDataUrl(null);
      return;
    }
    const q = new URLSearchParams({ name: conversation.name, public_key: pubkey, type: String(type) });
    QRCode.toDataURL(`meshcore://contact/add?${q}`, { width: 200, margin: 2, color: { dark: "#000000", light: "#ffffff" } })
      .then(setQrDataUrl)
      .catch(() => setQrDataUrl(null));
  }, [open, isContact, pubkey, conversation.name, type]);

  useEffect(() => {
    if (!open || !isChannel) {
      setChannelKey(null);
      return;
    }
    fetch(`/api/companions/${encodeURIComponent(companion)}/channels/${encodeURIComponent(conversation.name)}/key`)
      .then((r) => {
        if (!r.ok) throw new Error("fetch key");
        return r.json() as Promise<{ name: string; key: string }>;
      })
      .then(setChannelKey)
      .catch(() => toast.error("Could not load the channel key"));
  }, [open, isChannel, companion, conversation.name]);

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="rounded-none border-border bg-card max-w-sm">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">Share</DialogTitle>
          <DialogDescription className="font-mono text-xs text-muted-foreground">
            {isChannel ? `Share ${conversation.name}` : `Share ${conversation.name}'s contact`}
          </DialogDescription>
        </DialogHeader>
        {isChannel && channelKey && <ChannelShare name={channelKey.name} keyHex={channelKey.key} region={region} />}
        {isContact && pubkey && (
          <div className="space-y-3">
            {qrDataUrl && (
              <div className="flex justify-center">
                <img src={qrDataUrl} alt={`QR code to add ${conversation.name}`} className="size-48 border border-border" />
              </div>
            )}
            <div className="flex items-center gap-2">
              <code className="min-w-0 flex-1 break-all border border-border bg-background p-2 font-mono text-[11px] select-all">
                {pubkey}
              </code>
              <Button
                variant="ghost"
                size="sm"
                aria-label="Copy public key"
                onClick={async () => {
                  try {
                    await navigator.clipboard.writeText(pubkey);
                    toast.success("Public key copied");
                  } catch {
                    toast.error("Copy failed: select the key and copy it");
                  }
                }}
                className="h-8 shrink-0 rounded-none"
              >
                <ClipboardCopy className="size-3.5" />
              </Button>
            </div>
            <p className="font-mono text-[10px] text-muted-foreground/70">
              Share this public key so others can add this contact.
            </p>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

// ThreadRegionDialog sets the region a thread's sends flood in: the channel's, or the contact's for a DM or room; mounted only while open.
function ThreadRegionDialog({
  onClose,
  companion,
  companionId,
  conversation,
  onSaved,
}: {
  onClose: () => void;
  companion: string;
  companionId: number | null;
  conversation: Conversation;
  onSaved: () => void;
}) {
  const settings = useRegionSettings();
  const companionScope = useCompanionScope(companionId);
  const dm = conversation.channel.startsWith("dm:") ? conversation.channel.slice(3) : null;
  const channels = useApiList<ConfigChannel>(
    !dm && companionId != null ? `/api/config/companions/${companionId}/channels` : null,
    "Failed to load channels",
  );
  const contact = useApiObject<{ peerPubkey: string; floodScope: FloodScope }>(
    dm ? `/api/companions/${encodeURIComponent(companion)}/contacts/${dm}` : null,
    "Failed to load contact",
  );
  const row = channels.items?.find((c) => c.name === conversation.channel);
  const thisContact = contact.item?.peerPubkey.toLowerCase() === dm?.toLowerCase() ? contact.item : null;
  const value = dm ? thisContact?.floodScope : row?.floodScope;
  const [saving, setSaving] = useState(false);

  let problem: string | null = null;
  if (dm && contact.notFound) problem = "This node isn't a contact, so it sends in the companion's region.";
  else if (dm && contact.error) problem = contact.error;
  else if (!dm && channels.error) problem = channels.error;
  else if (!dm && channels.items && !row) problem = "This channel isn't in the companion's settings, so its region can't be set here.";

  const save = async (floodScope: FloodScope) => {
    setSaving(true);
    try {
      if (dm) {
        await request(`/api/companions/${encodeURIComponent(companion)}/contacts/${dm}/region`, "PUT", { floodScope });
        contact.setItem((c) => c && { ...c, floodScope });
      } else if (row) {
        await configApi.saveChannel(row.id, { name: row.name, floodScope });
        channels.setItems((rs) => rs?.map((r) => (r.id === row.id ? { ...r, floodScope } : r)) ?? rs);
      }
      toast.success("Region saved");
      onSaved();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save the region");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="rounded-none border-border bg-card max-w-sm">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">Region</DialogTitle>
          <DialogDescription className="font-mono text-xs text-muted-foreground">
            The region {conversation.name}&rsquo;s messages flood in. Repeaters that don&rsquo;t carry it won&rsquo;t relay them.
          </DialogDescription>
        </DialogHeader>
        {value ? (
          <RegionSelect
            label={dm ? "Region for this contact" : "Region for this channel"}
            value={value}
            onChange={(v) => void save(v)}
            regions={settings.regions}
            inherit={{ from: "companion", resolved: companionScope }}
            disabled={saving}
            hint={dm ? "also its logins and requests; a DM on a known route goes direct, with no region" : undefined}
          />
        ) : (
          <p role="status" className="font-mono text-xs text-muted-foreground">
            {problem ?? "Loading…"}
          </p>
        )}
      </DialogContent>
    </Dialog>
  );
}

function RenameDialog({
  open,
  onClose,
  companion,
  conversation,
}: {
  open: boolean;
  onClose: () => void;
  companion: string;
  conversation: Conversation;
}) {
  const [newName, setNewName] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (open) setNewName(conversation.name);
  }, [open, conversation.name]);

  const save = useCallback(async () => {
    if (!newName.trim() || saving) return;
    setSaving(true);
    try {
      const r = await fetch(
        `/api/companions/${encodeURIComponent(companion)}/channels/${encodeURIComponent(conversation.name)}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name: newName.trim() }),
        },
      );
      if (!r.ok) {
        const err = await r.json().catch(() => ({ error: "rename failed" }));
        throw new Error(err.error || "rename failed");
      }
      toast.success(`Channel renamed to "${newName.trim()}"`);
      onClose();
      window.location.reload();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Rename failed");
    } finally {
      setSaving(false);
    }
  }, [newName, saving, companion, conversation.name, onClose]);

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="rounded-none border-border bg-card max-w-sm">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">
            Rename Channel
          </DialogTitle>
          <DialogDescription className="font-mono text-xs text-muted-foreground">
            This does not affect the private key or channel functionality.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <Input
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
            placeholder="Channel name"
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            className="rounded-none border-border font-mono text-sm"
            onKeyDown={(e) => e.key === "Enter" && save()}
          />
          <div className="flex justify-end gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={onClose}
              className="rounded-none font-mono text-[11px] uppercase tracking-widest"
            >
              Cancel
            </Button>
            <Button
              size="sm"
              onClick={save}
              disabled={saving || !newName.trim() || newName.trim() === conversation.name}
              className="rounded-none font-mono text-[11px] uppercase tracking-widest"
            >
              {saving ? "Saving..." : "Rename"}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function ParticipantsDialog({
  open,
  onClose,
  companion,
  conversation,
}: {
  open: boolean;
  onClose: () => void;
  companion: string;
  conversation: Conversation;
}) {
  const [participants, setParticipants] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [blocking, setBlocking] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setLoading(true);
    fetch(
      `/api/companions/${encodeURIComponent(companion)}/conversations/${encodeURIComponent(conversation.id)}/participants`,
    )
      .then((r) => {
        if (!r.ok) throw new Error("fetch");
        return r.json();
      })
      .then((data: string[]) => setParticipants(data || []))
      .catch(() => toast.error("Failed to load participants"))
      .finally(() => setLoading(false));
  }, [open, companion, conversation.id]);

  const blockSender = useCallback(
    async (sender: string) => {
      setBlocking(sender);
      try {
        const r = await fetch(
          `/api/companions/${encodeURIComponent(companion)}/conversations/${encodeURIComponent(conversation.id)}/block`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ sender }),
          },
        );
        if (!r.ok) throw new Error("block");
        toast.success(`Blocked ${sender}`);
        setParticipants((prev) => prev.filter((p) => p !== sender));
      } catch {
        toast.error("Block failed");
      } finally {
        setBlocking(null);
      }
    },
    [companion, conversation.id],
  );

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="rounded-none border-border bg-card max-w-sm">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">
            Participants
          </DialogTitle>
          <DialogDescription className="font-mono text-xs text-muted-foreground">
            Senders seen in this channel
          </DialogDescription>
        </DialogHeader>
        <div className="max-h-[50dvh] overflow-y-auto space-y-1">
          {loading ? (
            <p className="font-mono text-xs text-muted-foreground/60 p-2">
              Loading...
            </p>
          ) : participants.length === 0 ? (
            <p className="font-mono text-xs text-muted-foreground/60 p-2">
              No participants found.
            </p>
          ) : (
            participants.map((p) => (
              <div
                key={p}
                className="flex items-center justify-between gap-2 px-2 py-1.5 border border-border bg-background"
              >
                <span className="font-mono text-xs truncate">{p}</span>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => blockSender(p)}
                  disabled={blocking === p}
                  className="rounded-none h-7 px-2 text-destructive hover:text-destructive font-mono text-[10px] uppercase tracking-[0.08em] shrink-0"
                >
                  <Ban className="size-3" />
                  Block
                </Button>
              </div>
            ))
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function BlockedSendersDialog({
  open,
  onClose,
  companion,
  conversation,
}: {
  open: boolean;
  onClose: () => void;
  companion: string;
  conversation: Conversation;
}) {
  const [blocked, setBlocked] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [unblocking, setUnblocking] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setLoading(true);
    fetch(
      `/api/companions/${encodeURIComponent(companion)}/conversations/${encodeURIComponent(conversation.id)}/block`,
    )
      .then((r) => {
        if (!r.ok) throw new Error("fetch");
        return r.json();
      })
      .then((data: string[]) => setBlocked(data || []))
      .catch(() => toast.error("Failed to load blocked senders"))
      .finally(() => setLoading(false));
  }, [open, companion, conversation.id]);

  const unblock = useCallback(
    async (sender: string) => {
      setUnblocking(sender);
      try {
        const r = await fetch(
          `/api/companions/${encodeURIComponent(companion)}/conversations/${encodeURIComponent(conversation.id)}/block/${encodeURIComponent(sender)}`,
          { method: "DELETE" },
        );
        if (!r.ok) throw new Error("unblock");
        toast.success(`Unblocked ${sender}`);
        setBlocked((prev) => prev.filter((b) => b !== sender));
      } catch {
        toast.error("Unblock failed");
      } finally {
        setUnblocking(null);
      }
    },
    [companion, conversation.id],
  );

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="rounded-none border-border bg-card max-w-sm">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">
            Blocked Senders
          </DialogTitle>
          <DialogDescription className="font-mono text-xs text-muted-foreground">
            Messages from blocked senders are hidden
          </DialogDescription>
        </DialogHeader>
        <div className="max-h-[50dvh] overflow-y-auto space-y-1">
          {loading ? (
            <p className="font-mono text-xs text-muted-foreground/60 p-2">
              Loading...
            </p>
          ) : blocked.length === 0 ? (
            <p className="font-mono text-xs text-muted-foreground/60 p-2">
              No blocked senders.
            </p>
          ) : (
            blocked.map((b) => (
              <div
                key={b}
                className="flex items-center justify-between gap-2 px-2 py-1.5 border border-border bg-background"
              >
                <span className="font-mono text-xs truncate">{b}</span>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => unblock(b)}
                  disabled={unblocking === b}
                  className="rounded-none h-7 px-2 font-mono text-[10px] uppercase tracking-[0.08em] shrink-0"
                >
                  <X className="size-3" />
                  Unblock
                </Button>
              </div>
            ))
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function DeleteHistoryDialog({
  open,
  onClose,
  companion,
  conversation,
  onDeleted,
}: {
  open: boolean;
  onClose: () => void;
  companion: string;
  conversation: Conversation;
  onDeleted: () => void;
}) {
  const [deleting, setDeleting] = useState(false);

  const doDelete = useCallback(async () => {
    setDeleting(true);
    try {
      const r = await fetch(
        `/api/companions/${encodeURIComponent(companion)}/conversations/${encodeURIComponent(conversation.id)}/messages`,
        { method: "DELETE" },
      );
      if (!r.ok) throw new Error("delete");
      toast.success("Message history cleared");
      onDeleted();
      onClose();
    } catch {
      toast.error("Failed to delete messages");
    } finally {
      setDeleting(false);
    }
  }, [companion, conversation.id, onDeleted, onClose]);

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="rounded-none border-border bg-card max-w-sm">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">
            Delete Message History
          </DialogTitle>
          <DialogDescription className="font-mono text-xs text-muted-foreground">
            This will permanently delete all messages in &ldquo;{conversation.name}&rdquo;.
            This action cannot be undone.
          </DialogDescription>
        </DialogHeader>
        <div className="flex justify-end gap-2 pt-2">
          <Button
            variant="ghost"
            size="sm"
            onClick={onClose}
            className="rounded-none font-mono text-[11px] uppercase tracking-widest"
          >
            Cancel
          </Button>
          <Button
            variant="destructive"
            size="sm"
            onClick={doDelete}
            disabled={deleting}
            className="rounded-none font-mono text-[11px] uppercase tracking-widest"
          >
            <Trash2 className="size-3" />
            {deleting ? "Deleting..." : "Delete All"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

