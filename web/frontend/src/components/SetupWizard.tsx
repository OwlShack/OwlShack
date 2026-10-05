import { useEffect, useState } from "react";
import {
  Radio,
  Loader2,
  ChevronLeft,
  ChevronRight,
  Check,
  Hash,
  CircleDashed,
} from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { PATH_HASH_SIZE_OPTIONS, TextField, SelectField } from "@/components/ConfigFields";
import { BACKEND_LABELS, backendFor, ConnectionFields, connectionSummary, useSerialPorts } from "@/components/ConnectionFields";
import { RadioPresetSelect } from "@/components/RadioPresetSelect";
import { PositionPicker } from "@/components/PositionPicker";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import { configApi, type Settings, type SpiBoard } from "@/lib/configApi";
import { RestoreEntryButton, RestoreFromBackup } from "@/components/RestoreFromBackup";

const BANDWIDTHS = [7.8, 10.4, 15.6, 20.8, 31.25, 41.7, 62.5, 125, 250, 500];

type Step = "welcome" | "restore" | "radio" | "companion" | "review";
const STEPS: { id: Step; label: string }[] = [
  { id: "welcome", label: "Welcome" },
  { id: "radio", label: "Radio" },
  { id: "companion", label: "Companion" },
  { id: "review", label: "Review" },
];

function StepDots({ current }: { current: Step }) {
  const activeIdx = STEPS.findIndex((s) => s.id === current);
  return (
    <div className="flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.12em]">
      {STEPS.map((s, i) => (
        <div key={s.id} className="flex items-center gap-2">
          <span
            className={cn(
              "inline-flex items-center gap-1.5",
              i === activeIdx
                ? "text-primary"
                : i < activeIdx
                  ? "text-muted-foreground"
                  : "text-muted-foreground/40",
            )}
          >
            <span
              className={cn(
                "grid size-4 place-items-center border text-[9px]",
                i === activeIdx
                  ? "border-primary text-primary"
                  : i < activeIdx
                    ? "border-muted-foreground/50 text-muted-foreground"
                    : "border-muted-foreground/30 text-muted-foreground/40",
              )}
            >
              {i < activeIdx ? <Check className="size-2.5" /> : i + 1}
            </span>
            <span className="hidden sm:inline">{s.label}</span>
          </span>
          {i < STEPS.length - 1 && (
            <span className="text-muted-foreground/30">/</span>
          )}
        </div>
      ))}
    </div>
  );
}

export function SetupWizard({
  settings,
  onComplete,
}: {
  settings: Settings;
  onComplete: () => void;
}) {
  const [step, setStep] = useState<Step>("welcome");
  const [busy, setBusy] = useState(false);

  // Radio (pre-filled from the bootstrapped defaults).
  const [connectionType, setConnectionType] = useState(
    backendFor(settings.connection ?? ""),
  );
  const [connection, setConnection] = useState(
    settings.connection ?? "serial:///dev/ttyACM0",
  );
  const [baudRate, setBaudRate] = useState(String(settings.baudRate ?? 115200));
  const [spiBoard, setSpiBoard] = useState(settings.spiBoard ?? "");
  const [modemToken, setModemToken] = useState("");
  const [boards, setBoards] = useState<SpiBoard[]>([]);
  const [freq, setFreq] = useState(
    settings.freq != null ? String(settings.freq) : "917.375",
  );
  const [bw, setBw] = useState(settings.bw != null ? String(settings.bw) : "62.5");
  const [sf, setSf] = useState(settings.sf != null ? String(settings.sf) : "7");
  const [cr, setCr] = useState(settings.cr != null ? String(settings.cr) : "8");
  const [tx, setTx] = useState(settings.tx != null ? String(settings.tx) : "22");
  const [pathHashSize, setPathHashSize] = useState(String(settings.pathHashSize ?? 1));

  // Companion.
  const [skipCompanion, setSkipCompanion] = useState(false);
  const [name, setName] = useState("");
  const [lat, setLat] = useState("");
  const [lon, setLon] = useState("");
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [privateKey, setPrivateKey] = useState("");
  const [advertInterval, setAdvertInterval] = useState("");

  const spi = connectionType === "spi";
  const ports = useSerialPorts(!spi);

  useEffect(() => {
    configApi
      .getSpiBoards()
      .then(setBoards)
      .catch(() => setBoards([]));
  }, []);

  const radioValid =
    connection.trim() !== "" &&
    // An SPI radio is driven by this process, so it cannot start without
    // knowing the board's wiring.
    (!spi || spiBoard !== "") &&
    Number.isFinite(parseFloat(freq)) &&
    bw !== "" &&
    sf !== "" &&
    cr !== "" &&
    Number.isFinite(parseInt(tx, 10));

  const finish = async () => {
    setBusy(true);
    try {
      // Before setupComplete, so a failure here keeps the wizard open instead of stranding a half-configured install.
      if (!skipCompanion && name.trim()) {
        await configApi.saveCompanion({
          name: name.trim(),
          latitude: lat === "" ? null : parseFloat(lat) || 0,
          longitude: lon === "" ? null : parseFloat(lon) || 0,
          privateKey: privateKey.trim() || undefined,
          advertInterval: advertInterval.trim()
            ? parseInt(advertInterval, 10) || 0
            : undefined,
        });
      }
      await configApi.putSettings({
        // Round-trip the values the wizard doesn't edit so saving setupComplete never clears them.
        connectionType,
        logLevel: settings.logLevel,
        listenAddr: settings.listenAddr,
        connection: connection.trim(),
        baudRate: parseInt(baudRate, 10) || 115200,
        ...(spi ? { spiBoard } : {}),
        freq: parseFloat(freq) || null,
        bw: parseFloat(bw) || null,
        sf: parseInt(sf, 10) || null,
        cr: parseInt(cr, 10) || null,
        tx: tx === "" ? null : parseInt(tx, 10),
        pathHashSize: parseInt(pathHashSize, 10) || 1,
        dutyCycle: settings.dutyCycle,
        packetRetentionDays: settings.packetRetentionDays,
        ...(modemToken !== "" ? { modemToken } : {}),
        setupComplete: true,
      });
      // Writes are validated and reloaded server-side before returning, so the re-fetched config already gates this wizard away.
      onComplete();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Setup failed");
      setBusy(false);
    }
  };

  return (
    <Dialog open onOpenChange={() => {}}>
      <DialogContent
        showCloseButton={false}
        className="rounded-none border-border sm:max-w-2xl max-h-[88dvh] overflow-y-auto gap-5"
      >
        <DialogHeader className="gap-3">
          <div className="flex items-center gap-2.5">
            <div className="relative grid size-8 shrink-0 place-items-center rounded-sm border border-primary/30 bg-primary/10">
              <Radio className="size-4 text-primary" strokeWidth={2} />
            </div>
            <div className="flex flex-col leading-tight">
              <span className="label-overline">OwlShack · first-run setup</span>
              <DialogTitle className="font-mono text-sm uppercase tracking-widest">
                Get on the air
              </DialogTitle>
            </div>
          </div>
          {step !== "restore" && <StepDots current={step} />}
        </DialogHeader>

        {step === "restore" && <RestoreFromBackup onRestored={onComplete} />}

        {step === "welcome" && (
          <div className="space-y-4">
            <p className="font-mono text-sm leading-relaxed text-muted-foreground">
              Welcome. Two quick steps: configure the radio, then create your
              first companion (the identity you run on the mesh).
            </p>
            <p className="font-mono text-xs leading-relaxed text-muted-foreground/70">
              Moving from another node? Restore its backup instead — this is the
              only time you can, so a running node is never overwritten.
            </p>
            <p className="font-mono text-xs leading-relaxed text-muted-foreground/70">
              Nothing is broadcast until you create a companion. You can skip
              the companion step to just watch live mesh traffic in this
              console; the radio still needs to be configured to connect.
            </p>
          </div>
        )}

        {step === "radio" && (
          <div className="space-y-5">
            <div className="space-y-3">
              <span className="label-overline">
                {`${BACKEND_LABELS[connectionType] ?? "kiss modem"} · connection`}
              </span>
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                <ConnectionFields
                  connectionType={connectionType}
                  setConnectionType={setConnectionType}
                  connection={connection}
                  setConnection={setConnection}
                  baudRate={baudRate}
                  setBaudRate={setBaudRate}
                  spiBoard={spiBoard}
                  setSpiBoard={setSpiBoard}
                  boards={boards}
                  modemToken={modemToken}
                  setModemToken={setModemToken}
                  modemTokenSet={settings.modemTokenSet}
                />
              </div>
            </div>
            <div className="space-y-3">
              <span className="label-overline">rf parameters · lora radio</span>
              <RadioPresetSelect
                freq={freq}
                bw={bw}
                sf={sf}
                cr={cr}
                onApply={(p) => {
                  setFreq(String(p.freq));
                  setBw(String(p.bw));
                  setSf(String(p.sf));
                  setCr(String(p.cr));
                  // Only when the preset states one; most do not, and defaulting would override a
                  // region that needs 2 bytes with the 1-byte default.
                  if (p.pathHashSize) setPathHashSize(String(p.pathHashSize));
                }}
              />
              <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
                <TextField
                  label="Frequency (MHz)"
                  value={freq}
                  onChange={setFreq}
                  placeholder="917.375"
                />
                <SelectField
                  label="Bandwidth (kHz)"
                  value={bw}
                  options={BANDWIDTHS.map((b) => ({
                    value: String(b),
                    label: `${b} kHz`,
                  }))}
                  onChange={setBw}
                />
                <SelectField
                  label="Spreading factor"
                  value={sf}
                  options={Array.from({ length: 8 }, (_, i) => ({
                    value: String(i + 5),
                    label: `SF${i + 5}`,
                  }))}
                  onChange={setSf}
                />
                <SelectField
                  label="Coding rate"
                  value={cr}
                  options={Array.from({ length: 4 }, (_, i) => ({
                    value: String(i + 5),
                    label: `4/${i + 5}`,
                  }))}
                  onChange={setCr}
                />
                <TextField
                  label="TX power (dBm)"
                  value={tx}
                  onChange={setTx}
                  placeholder="0-22"
                />
                <SelectField
                  label="Path hash size"
                  value={pathHashSize}
                  options={PATH_HASH_SIZE_OPTIONS}
                  onChange={setPathHashSize}
                  hint="must match the rest of your mesh · some regions run 2 bytes"
                />
              </div>
            </div>
            {!radioValid && (
              <p className="font-mono text-[10px] text-warning">
                Fill in the connection and all RF parameters to continue.
              </p>
            )}
          </div>
        )}

        {step === "companion" && (
          <div className="space-y-5">
            <TextField
              label="Companion name"
              value={name}
              onChange={setName}
              placeholder="e.g. KO6XYZ-1 or Base Station"
              hint="shown on the mesh and in adverts · choose something recognisable"
            />
            <div className="grid grid-cols-2 gap-4">
              <TextField
                label="Latitude"
                value={lat}
                onChange={setLat}
                placeholder="blank = no position"
              />
              <TextField
                label="Longitude"
                value={lon}
                onChange={setLon}
                placeholder="blank = no position"
              />
            </div>
            <PositionPicker
              lat={parseFloat(lat)}
              lon={parseFloat(lon)}
              onPick={(la, lo) => {
                setLat(la.toFixed(6));
                setLon(lo.toFixed(6));
              }}
            />
            <div className="flex items-center gap-1.5 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/70">
              <Hash className="size-3 text-primary" />
              joins the <span className="text-foreground">Public</span> channel
              automatically
            </div>

            {showAdvanced ? (
              <div className="space-y-4 border-t border-border pt-4">
                <span className="label-overline">advanced · optional</span>
                <TextField
                  label="Private key"
                  type="password"
                  value={privateKey}
                  onChange={setPrivateKey}
                  placeholder="blank = generate a new identity"
                  hint="64-hex ed25519 seed · leave blank to auto-generate"
                />
                <TextField
                  label="Advert interval (s)"
                  value={advertInterval}
                  onChange={setAdvertInterval}
                  placeholder="blank = 86400 (daily)"
                  hint="0 = never advertise"
                />
              </div>
            ) : (
              <button
                type="button"
                onClick={() => setShowAdvanced(true)}
                className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground hover:text-foreground"
              >
                + advanced (identity key, advert interval)
              </button>
            )}
          </div>
        )}

        {step === "review" && (
          <div className="space-y-4">
            <div className="border border-border bg-card divide-y divide-border font-mono text-xs">
              {connectionSummary(connectionType, connection, baudRate, spiBoard, boards, ports, modemToken !== "" || settings.modemTokenSet).map(
                (row) => (
                  <div key={row.label} className="flex justify-between gap-4 px-3 py-2">
                    <span className="shrink-0 text-muted-foreground uppercase tracking-[0.08em]">
                      {row.label}
                    </span>
                    <span className="min-w-0 truncate text-right">
                      {row.value}
                      {row.sub && <span className="text-muted-foreground"> · {row.sub}</span>}
                    </span>
                  </div>
                ),
              )}
              <div className="flex justify-between gap-4 px-3 py-2">
                <span className="shrink-0 text-muted-foreground uppercase tracking-[0.08em]">
                  LoRa Radio
                </span>
                <span className="min-w-0 tabular-nums text-right">
                  {freq} MHz · {bw} kHz · SF{sf} · 4/{cr} · {tx} dBm · {pathHashSize}B path
                </span>
              </div>
              <div className="flex justify-between gap-4 px-3 py-2">
                <span className="shrink-0 text-muted-foreground uppercase tracking-[0.08em]">
                  Companion
                </span>
                <span className="min-w-0 text-right">
                  {skipCompanion || !name.trim() ? (
                    <span className="inline-flex items-center gap-1.5 text-muted-foreground">
                      <CircleDashed className="size-3" /> view-only
                    </span>
                  ) : (
                    <span>
                      {name.trim()}
                      <span className="text-muted-foreground">
                        {" "}
                        · #Public
                        {lat && lon ? ` · ${lat}, ${lon}` : ""}
                      </span>
                    </span>
                  )}
                </span>
              </div>
            </div>
            <p className="font-mono text-[10px] text-muted-foreground/70">
              {skipCompanion || !name.trim()
                ? "No companion is created. You can watch live mesh traffic in this console, but you won't appear on the mesh, and MQTT feeds (LetsMesh, CoreScope) need a companion. Add one any time from the Companions page."
                : "The companion starts immediately and begins advertising on the mesh."}
            </p>
          </div>
        )}

        {/* Footer / navigation */}
        <div className="flex items-center justify-between gap-2 border-t border-border pt-4">
          <div>
            {step !== "welcome" && (
              <Button
                variant="ghost"
                size="sm"
                disabled={busy}
                onClick={() =>
                  setStep(
                    step === "radio" || step === "restore"
                      ? "welcome"
                      : step === "companion"
                        ? "radio"
                        : "companion",
                  )
                }
                className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
              >
                <ChevronLeft className="size-3.5" /> back
              </Button>
            )}
          </div>

          <div className="flex items-center gap-2">
            {step === "companion" && (
              <Button
                variant="ghost"
                size="sm"
                disabled={busy}
                onClick={() => {
                  setSkipCompanion(true);
                  setStep("review");
                }}
                className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em] text-muted-foreground"
              >
                skip · view-only
              </Button>
            )}

            {step === "welcome" && (
              <RestoreEntryButton onClick={() => setStep("restore")} />
            )}
            {step === "welcome" && (
              <Button
                size="sm"
                onClick={() => setStep("radio")}
                className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
              >
                begin <ChevronRight className="size-3.5" />
              </Button>
            )}
            {step === "radio" && (
              <Button
                size="sm"
                disabled={!radioValid}
                onClick={() => setStep("companion")}
                className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
              >
                next <ChevronRight className="size-3.5" />
              </Button>
            )}
            {step === "companion" && (
              <Button
                size="sm"
                disabled={!name.trim()}
                onClick={() => {
                  setSkipCompanion(false);
                  setStep("review");
                }}
                className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
              >
                review <ChevronRight className="size-3.5" />
              </Button>
            )}
            {step === "review" && (
              <Button
                size="sm"
                disabled={busy}
                onClick={finish}
                className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
              >
                {busy ? (
                  <Loader2 className="size-3.5 animate-spin" />
                ) : (
                  <Check className="size-3.5" />
                )}
                finish
              </Button>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
