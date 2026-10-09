import { useCallback, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { CircleDashed, Star, UserPlus } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { HeaderButton } from "@/components/HeaderButton";
import { Skeleton } from "@/components/ui/skeleton";
import { useApiList } from "@/hooks/useApiList";
import { useCompanionRef, useCompanions } from "@/hooks/useCompanions";
import { BackLink } from "@/components/BackLink";
import { InlineConfirm } from "@/components/InlineConfirm";
import { LoadErrorAlert } from "@/components/LoadErrorAlert";
import { PageHeader } from "@/components/PageHeader";
import { PeerAvatar } from "@/components/PeerAvatar";
import { PeerTypePill } from "@/components/StatusIndicator";
import { AddContactDialog } from "@/components/AddContactDialog";
import { timeAgo, truncateMid } from "@/lib/format";
import { contactDetailPath } from "@/lib/routes";
import { apiErrorMessage } from "@/lib/apiError";
import { cn } from "@/lib/utils";

interface Contact {
  peerPubkey: string;
  name: string;
  type?: string;
  addedAt: string;
  // favourite is the MeshCore app's star; the app and this page set the same one.
  metadata: { favourite?: boolean };
}

export function ContactsPage() {
  const { ref } = useParams();
  const {
    ref: companion,
    id: companionId,
    name: companionName,
  } = useCompanionRef(ref);

  const {
    items: contacts,
    loading,
    error,
    reload: load,
  } = useApiList<Contact>(
    companion
      ? `/api/companions/${encodeURIComponent(companion)}/contacts`
      : null,
    "Failed to load contacts",
  );
  const [confirmRemove, setConfirmRemove] = useState<string | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  // The companion's own pubkey, so the add dialog can block a self-contact inline.
  const ownPubkey = useCompanions().find((c) => c.id === companionId)?.pubkey;

  const removeContact = useCallback(
    async (pubkey: string) => {
      try {
        const res = await fetch(
          `/api/companions/${encodeURIComponent(companion)}/contacts/${pubkey}`,
          { method: "DELETE" },
        );
        if (!res.ok) {
          const err = await res.json().catch(() => ({}));
          throw new Error(err.error || `HTTP ${res.status}`);
        }
        toast.success("Contact removed");
        setConfirmRemove(null);
        load();
      } catch (e) {
        const msg = e instanceof Error ? e.message : "failed";
        toast.error(`Failed to remove contact: ${msg}`);
      }
    },
    [companion, load],
  );

  // The star shows at once, but the order stays as loaded until the next load, so no row moves from under the pointer or the keyboard.
  const [stars, setStars] = useState<{ list: Contact[] | null; on: Record<string, boolean> }>({ list: null, on: {} });
  const starred = stars.list === contacts ? stars.on : {};
  const setStar = useCallback(
    (pubkey: string, on: boolean) =>
      setStars((s) => ({ list: contacts, on: { ...(s.list === contacts ? s.on : {}), [pubkey]: on } })),
    [contacts],
  );
  const isFavourite = (c: Contact) => starred[c.peerPubkey] ?? !!c.metadata.favourite;

  const saving = useRef(new Set<string>());
  const toggleFavourite = useCallback(
    async (c: Contact, was: boolean) => {
      if (saving.current.has(c.peerPubkey)) return;
      saving.current.add(c.peerPubkey);
      setStar(c.peerPubkey, !was);
      try {
        const res = await fetch(
          `/api/companions/${encodeURIComponent(companion)}/contacts/${c.peerPubkey}`,
          { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ favourite: !was }) },
        );
        if (!res.ok) throw new Error(await apiErrorMessage(res, "Failed to save the favourite"));
      } catch (e) {
        setStar(c.peerPubkey, was);
        toast.error(e instanceof Error ? e.message : "Failed to save the favourite");
      } finally {
        saving.current.delete(c.peerPubkey);
      }
    },
    [companion, setStar],
  );

  const contactsSorted = useMemo(() => {
    if (!contacts) return [];
    return [...contacts].sort(
      (a, b) =>
        Number(!!b.metadata.favourite) - Number(!!a.metadata.favourite) ||
        (a.name || "").localeCompare(b.name || ""),
    );
  }, [contacts]);

  const existingPubkeys = useMemo(
    () => (contacts || []).map((c) => c.peerPubkey),
    [contacts],
  );

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-3">
        <BackLink
          to={`/companions/${encodeURIComponent(companion)}`}
          label={companionName || "companion"}
        />

        <PageHeader
          title="Contacts"
          meta={
            contacts && (
              <span className="font-mono text-sm text-muted-foreground tabular-nums">
                {contacts.length} configured
              </span>
            )
          }
          actions={
            <HeaderButton tone="primary" icon={UserPlus} onClick={() => setDialogOpen(true)}>
              add contact
            </HeaderButton>
          }
          className="mb-0"
        />
      </div>

      {loading && <ContactsSkeleton />}

      {error && <LoadErrorAlert message={error} onRetry={load} />}

      {!loading && !error && contacts && (
        <section className="panel overflow-hidden">
          <div className="flex items-center justify-between px-4 py-3 border-b border-border">
            <div className="space-y-0.5">
              <span className="label-overline block">Roster</span>
              <h2 className="font-mono text-sm uppercase tracking-widest">
                Allowed peers
              </h2>
            </div>
            <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/70 tabular-nums">
              {contacts.length}
            </span>
          </div>

          {contactsSorted.length === 0 ? (
            <div className="px-6 py-16 text-center">
              <CircleDashed className="size-8 mx-auto mb-3 text-muted-foreground/40" />
              <p className="font-mono text-sm uppercase tracking-widest text-muted-foreground">
                No contacts yet
              </p>
              <p className="mt-2 text-xs text-muted-foreground/70">
                Add peers to enable direct messaging.
              </p>
              <Button
                variant="default"
                size="sm"
                onClick={() => setDialogOpen(true)}
                className="mt-4 font-mono text-xs uppercase tracking-widest"
              >
                <UserPlus className="size-3.5" />
                Add contact
              </Button>
            </div>
          ) : (
            <div className="divide-y divide-border">
              {contactsSorted.map((c) => (
                <ContactRow
                  key={c.peerPubkey}
                  companion={companion}
                  contact={c}
                  favourite={isFavourite(c)}
                  onToggleFavourite={() => void toggleFavourite(c, isFavourite(c))}
                  confirming={confirmRemove === c.peerPubkey}
                  onAskRemove={() => setConfirmRemove(c.peerPubkey)}
                  onCancel={() => setConfirmRemove(null)}
                  onConfirm={() => removeContact(c.peerPubkey)}
                />
              ))}
            </div>
          )}
        </section>
      )}

      <AddContactDialog
        companion={companion}
        companionName={companionName}
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        existingPubkeys={existingPubkeys}
        ownPubkey={ownPubkey}
        onAdded={load}
      />
    </div>
  );
}

function ContactRow({
  companion,
  contact,
  favourite,
  onToggleFavourite,
  confirming,
  onAskRemove,
  onCancel,
  onConfirm,
}: {
  companion: string;
  contact: Contact;
  favourite: boolean;
  onToggleFavourite: () => void;
  confirming: boolean;
  onAskRemove: () => void;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const displayName = contact.name || "unknown peer";
  const detailTo = contactDetailPath(companion, contact.peerPubkey, contact.type);
  return (
    <div className="flex items-center gap-2 sm:gap-4 px-3 sm:px-4 py-3 hover:bg-muted/40 transition-colors">
      <Link to={detailTo} className="flex items-center gap-3 min-w-0 flex-1">
        <PeerAvatar name={displayName} size="md" />

        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 min-w-0">
            <span className="text-sm font-medium truncate">
              {contact.name || (
                <span className="text-muted-foreground italic">unknown</span>
              )}
            </span>
            {contact.type && (
              <span className="shrink-0">
                <PeerTypePill type={contact.type} />
              </span>
            )}
          </div>
          <code className="font-mono text-xs text-muted-foreground block truncate tabular-nums">
            {truncateMid(contact.peerPubkey, 8, 6)} · added {timeAgo(contact.addedAt)}
          </code>
        </div>
      </Link>

      <div className="flex items-center gap-1 shrink-0">
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={onToggleFavourite}
          aria-pressed={favourite}
          aria-label={`Favourite ${displayName}`}
          className={cn(favourite ? "text-primary hover:text-primary/80" : "text-muted-foreground/60 hover:text-foreground")}
        >
          <Star className={cn("size-3.5", favourite && "fill-current")} />
        </Button>
        <InlineConfirm
          confirming={confirming}
          onAskRemove={onAskRemove}
          onCancel={onCancel}
          onConfirm={onConfirm}
          iconOnly
          ariaLabel="Remove contact"
        />
      </div>
    </div>
  );
}

function ContactsSkeleton() {
  return (
    <div className="panel">
      <div className="px-4 py-3 border-b border-border">
        <Skeleton className="h-3 w-20 mb-2" />
        <Skeleton className="h-4 w-32" />
      </div>
      <div className="divide-y divide-border">
        {[...Array(4)].map((_, i) => (
          <div key={i} className="flex items-center gap-3 px-4 py-3">
            <Skeleton className="size-9" />
            <div className="flex-1 space-y-2">
              <Skeleton className="h-3 w-32" />
              <Skeleton className="h-3 w-48" />
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
