import { Globe, Hash, Lock } from "lucide-react";
import { cn } from "@/lib/utils";

export type ChannelKind = "public" | "hashtag" | "private";

// channelKind: a channel with its own key is private; with none it is Public or a "#name" whose key anyone can derive from the name.
export function channelKind(name: string, privateKeySet: boolean | undefined): ChannelKind {
  if (privateKeySet ?? (!name.startsWith("#") && name.toLowerCase() !== "public")) return "private";
  return name.startsWith("#") ? "hashtag" : "public";
}

const kinds = {
  public: { Icon: Globe, label: "public channel", hint: "the mesh's shared Public key" },
  hashtag: { Icon: Hash, label: "hashtag channel", hint: "keyed by its name: anyone who knows the name can read it" },
  private: { Icon: Lock, label: "private channel", hint: "its own key: only those you share it with can read it" },
} as const;

export function ChannelKindIcon({ kind, className }: { kind: ChannelKind; className?: string }) {
  const { Icon, label, hint } = kinds[kind];
  return (
    <span title={`${label}: ${hint}`} aria-hidden className={cn("inline-flex shrink-0", className)}>
      <Icon className="size-3" />
    </span>
  );
}

export const channelKindLabel = (kind: ChannelKind) => kinds[kind].label;
