import { useEffect, useState } from "react";
import { ClipboardCopy } from "lucide-react";
import QRCode from "qrcode";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";

// channelShareLink is the MeshCore app's "add channel" link, which its QR scanner reads; the region goes with the channel when it has one.
export function channelShareLink(name: string, keyHex: string, region: string | null): string {
  const q = new URLSearchParams({ name, secret: keyHex.toLowerCase() });
  if (region) q.set("region_scope", region.startsWith("#") ? region : `#${region}`);
  return `meshcore://channel/add?${q}`;
}

// ChannelShare shows a channel's QR and key, as the MeshCore app's Share Channel screen does.
export function ChannelShare({ name, keyHex, region }: { name: string; keyHex: string; region: string | null }) {
  const [qr, setQr] = useState<string | null>(null);
  const link = channelShareLink(name, keyHex, region);
  useEffect(() => {
    let live = true;
    QRCode.toDataURL(link, { width: 200, margin: 2, color: { dark: "#000000", light: "#ffffff" } })
      .then((url) => live && setQr(url))
      .catch(() => live && setQr(null));
    return () => {
      live = false;
    };
  }, [link]);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(keyHex);
      toast.success("Key copied");
    } catch {
      toast.error("Copy failed: select the key and copy it");
    }
  };
  return (
    <div className="space-y-3">
      {qr && (
        <div className="flex justify-center">
          <img src={qr} alt={`QR code to add ${name}`} className="size-48 border border-border" />
        </div>
      )}
      <p className="text-center font-mono text-[10px] text-muted-foreground/70">
        In the MeshCore app: Menu → Add Channel → Scan QR Code{region ? ` · region ${region.replace(/^#/, "")}` : ""}
      </p>
      <div className="flex items-center gap-2">
        <code className="min-w-0 flex-1 break-all border border-border bg-background p-2 font-mono text-[11px] select-all">
          {keyHex}
        </code>
        <Button variant="ghost" size="sm" onClick={copy} aria-label="Copy key" className="h-8 shrink-0 rounded-none">
          <ClipboardCopy className="size-3.5" />
        </Button>
      </div>
      <p className="font-mono text-[10px] text-muted-foreground/70">
        Anyone with the key can read and send in this channel.
      </p>
    </div>
  );
}
