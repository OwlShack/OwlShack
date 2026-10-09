import { useId } from "react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";

export function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1">
      <Label className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
        {label}
      </Label>
      {children}
      {hint && (
        <p className="font-mono text-[10px] text-muted-foreground/60">{hint}</p>
      )}
    </div>
  );
}

export function TextField({
  label,
  value,
  onChange,
  placeholder,
  hint,
  type = "text",
  disabled,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  hint?: React.ReactNode;
  type?: string;
  disabled?: boolean;
}) {
  return (
    <Field label={label} hint={hint}>
      <Input
        type={type}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        disabled={disabled}
        className="h-9 font-mono text-base md:text-sm rounded-none border-border bg-background"
      />
    </Field>
  );
}

// Radix reads a "" value as "show the placeholder", so an option meaning "none" or "inherit" would render blank.
const EMPTY = "\u0000empty";
const toSelect = (v: string) => (v === "" ? EMPTY : v);

export function SelectField({
  label,
  value,
  options,
  onChange,
  hint,
  disabled,
}: {
  label: string;
  value: string;
  // depth indents an option under the one before it, for a list that is a tree.
  options: { value: string; label: string; depth?: number }[];
  onChange: (v: string) => void;
  hint?: React.ReactNode;
  disabled?: boolean;
}) {
  // Tolerate a device/config value outside the curated set.
  const known = options.some((o) => o.value === value);
  return (
    <Field label={label} hint={hint}>
      {/* Inside a form Radix adds a hidden native select; the div keeps it from being Field's last child, which takes the trigger's space-y margin. */}
      <div>
        <Select value={toSelect(value)} onValueChange={(v) => onChange(v === EMPTY ? "" : v)} disabled={disabled}>
          <SelectTrigger className="h-9 w-full font-mono text-sm rounded-none border-border bg-background">
            <SelectValue />
          </SelectTrigger>
          <SelectContent className="rounded-sm">
            {!known && value !== "" && (
              <SelectItem value={value} className="font-mono text-sm">
                {value} (custom)
              </SelectItem>
            )}
            {options.map((o) => (
              <SelectItem
                key={o.value}
                value={toSelect(o.value)}
                className="font-mono text-sm"
                style={o.depth ? { paddingLeft: `${0.5 + o.depth * 1.25}rem` } : undefined}
              >
                {o.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    </Field>
  );
}

export function SwitchRow({
  label,
  hint,
  checked,
  onChange,
  disabled,
}: {
  label: string;
  hint?: string;
  checked: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
}) {
  const hintId = useId();
  return (
    <label className="flex items-center justify-between gap-3 px-3 py-2 bg-card border border-border cursor-pointer">
      <div className="min-w-0">
        <div className="font-mono text-xs uppercase tracking-[0.08em]">
          {label}
        </div>
        {hint && (
          <div id={hintId} className="font-mono text-[10px] leading-snug text-muted-foreground/60">
            {hint}
          </div>
        )}
      </div>
      <Switch
        checked={checked}
        onCheckedChange={onChange}
        disabled={disabled}
        aria-label={label}
        aria-describedby={hint ? hintId : undefined}
      />
    </label>
  );
}

// Per-hop hash width in bytes; the firmware's `set path.hash.mode` takes this minus one and accepts 0-2.
export const PATH_HASH_SIZE_OPTIONS = [1, 2, 3].map((n) => ({
  value: String(n),
  label: `${n} byte${n > 1 ? "s" : ""}`,
}));
