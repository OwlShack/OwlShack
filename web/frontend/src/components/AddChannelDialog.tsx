import { useEffect, useState } from "react";
import { CircleDashed, Dices, Hash, Lock, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ChannelShare } from "@/components/ChannelShare";

export interface Channel {
  name: string;
}

// Firmware companions and the MeshCore app take only 16-byte channel keys.
const KEY_HEX = /^[0-9a-fA-F]{32}$/;

const newKey = () => Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, "0")).join("");

type ChannelMode = "public" | "private";

/** Presentational: the caller supplies `onAdd` (the POST) and `existing` for the duplicate-name guard. */
export function AddChannelDialog({
  open,
  onOpenChange,
  existing,
  onAdd,
  prefillName,
  region,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  existing: Channel[];
  onAdd: (name: string, privateKey?: string) => Promise<void>;
  prefillName?: string;
  // region is where the new channel will send, which its share carries.
  region: string | null;
}) {
  const [mode, setMode] = useState<ChannelMode>("public");
  const [channelName, setChannelName] = useState("");
  const [privateKey, setPrivateKey] = useState("");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (open) {
      setMode("public");
      setChannelName(prefillName ?? "");
      setPrivateKey("");
      setSubmitting(false);
    }
  }, [open, prefillName]);

  const trimmedName = channelName.trim();
  const trimmedKey = privateKey.trim();
  // The key derives from the exact name, so "#Foo" and "#foo" are distinct channels.
  const nameTaken = existing.some((c) => c.name === trimmedName);
  const nameValid = trimmedName.length > 0 && !nameTaken;
  const keyValid = mode === "public" || KEY_HEX.test(trimmedKey);

  const canSubmit = nameValid && keyValid && !submitting;

  const handleSubmit = async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    if (mode === "private") {
      await onAdd(trimmedName, trimmedKey);
    } else {
      await onAdd(trimmedName);
    }
    setSubmitting(false);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="rounded-none border-border bg-card max-w-md">
        <DialogHeader>
          <DialogTitle className="font-mono uppercase tracking-[0.08em] text-sm">
            Add channel
          </DialogTitle>
          <DialogDescription className="text-xs text-muted-foreground">
            Join a hashtag channel by name, or a private one by its key, or make a new private channel.
          </DialogDescription>
        </DialogHeader>

        <Tabs
          value={mode}
          onValueChange={(v) => setMode(v as ChannelMode)}
          className="gap-3"
        >
          <TabsList className="rounded-none bg-muted h-9 grid grid-cols-2 w-full">
            <TabsTrigger
              value="public"
              className="rounded-none font-mono text-[11px] uppercase tracking-widest gap-1.5"
            >
              <Hash className="size-3" />
              hashtag
            </TabsTrigger>
            <TabsTrigger
              value="private"
              className="rounded-none font-mono text-[11px] uppercase tracking-widest gap-1.5"
            >
              <Lock className="size-3" />
              private
            </TabsTrigger>
          </TabsList>

          <TabsContent value="public" className="mt-0 space-y-3">
            <ChannelNameField
              value={channelName}
              onChange={setChannelName}
              taken={nameTaken && trimmedName.length > 0}
            />
            <p className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/70">
              The key comes from the name, so anyone who knows it can read the channel.
            </p>
          </TabsContent>

          <TabsContent value="private" className="mt-0 space-y-3">
            <ChannelNameField
              value={channelName}
              onChange={setChannelName}
              taken={nameTaken && trimmedName.length > 0}
            />
            <div className="space-y-1.5">
              <Label
                htmlFor="channel-key"
                className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground"
              >
                Private key
              </Label>
              <div className="flex gap-2">
                <Input
                  id="channel-key"
                  value={privateKey}
                  onChange={(e) => setPrivateKey(e.target.value)}
                  placeholder="paste a key, or generate one"
                  spellCheck={false}
                  autoCorrect="off"
                  autoCapitalize="off"
                  aria-invalid={trimmedKey.length > 0 && !KEY_HEX.test(trimmedKey)}
                  className="h-10 min-w-0 flex-1 rounded-none font-mono text-xs sm:h-8"
                />
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setPrivateKey(newKey())}
                  className="h-10 shrink-0 rounded-none font-mono text-[11px] uppercase tracking-widest sm:h-8"
                >
                  <Dices className="size-3" />
                  generate
                </Button>
              </div>
              {trimmedKey.length > 0 && !KEY_HEX.test(trimmedKey) && (
                <p className="text-[10px] text-destructive font-mono">
                  must be 32 hex characters
                </p>
              )}
              {trimmedKey.length === 0 && (
                <p className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/70">
                  Joining a private channel? Paste its key. Starting one? Generate a key and share it.
                </p>
              )}
            </div>
            {trimmedName && KEY_HEX.test(trimmedKey) && (
              <ChannelShare name={trimmedName} keyHex={trimmedKey} region={region} />
            )}
          </TabsContent>
        </Tabs>

        <DialogFooter className="mt-2">
          <Button
            variant="ghost"
            size="sm"
            onClick={() => onOpenChange(false)}
            className="font-mono uppercase tracking-widest"
          >
            cancel
          </Button>
          <Button
            variant="default"
            size="sm"
            disabled={!canSubmit}
            onClick={handleSubmit}
            className="font-mono uppercase tracking-widest"
          >
            {submitting ? (
              <>
                <CircleDashed className="size-3 animate-spin" />
                adding…
              </>
            ) : (
              <>
                <Plus className="size-3" />
                add channel
              </>
            )}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ChannelNameField({
  value,
  onChange,
  taken,
}: {
  value: string;
  onChange: (v: string) => void;
  taken: boolean;
}) {
  return (
    <div className="space-y-1.5">
      <Label
        htmlFor="channel-name"
        className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground"
      >
        Channel name
      </Label>
      <Input
        id="channel-name"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder="general"
        autoCapitalize="off"
        spellCheck={false}
        aria-invalid={taken}
        className="rounded-none font-mono text-xs h-10 sm:h-8"
      />
      {taken && (
        <p className="text-[10px] text-destructive font-mono">
          channel already configured
        </p>
      )}
    </div>
  );
}
