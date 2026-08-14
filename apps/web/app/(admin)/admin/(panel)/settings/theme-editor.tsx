"use client";

import { Alert, AlertDescription, AlertTitle } from "@omniflow/ui/alert";
import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@omniflow/ui/tabs";
import { toast } from "@omniflow/ui/toast";
import { Trash2, Upload } from "lucide-react";
import { useTranslations } from "next-intl";
import { type CSSProperties, useId, useRef, useState } from "react";
import useSWR from "swr";

import { PageHeader } from "@/components/admin/resource-table";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import type { BrandingAssets, Palette, ThemeSettings } from "@/lib/operations";
import { useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";
import { useUnsavedChanges } from "@/lib/use-unsaved-changes";

const MODES = ["light", "dark"] as const;
const RADII = ["square", "compact", "default", "rounded"] as const;
const DENSITIES = ["compact", "default", "comfortable"] as const;

/**
 * The white-label screen: an installation's own colours, corners, spacing, and
 * mark.
 *
 * Two rules shape it, and both come from the API rather than from here.
 *
 * The token list is published by the server, so this screen offers exactly the
 * slots this build honours instead of a list of its own that could drift past
 * one. And contrast is judged server-side: the ratios and the refusal come back
 * with the settings, because a second implementation of the WCAG formula in the
 * browser would eventually disagree with the one that decides whether a save is
 * allowed.
 *
 * What the browser does own is the preview. Every token is a CSS custom
 * property, so setting the form's current values on one wrapper element shows
 * the operator their palette on real components before anything is stored.
 */
export function ThemeEditor() {
  const translate = useTranslations("admin.branding");
  const { can } = useSession();
  const editable = can("settings.write");

  const { data, isLoading, mutate } = useSWR<ThemeSettings, ApiError>(
    "/v1/panel/settings/theme",
    fetcher,
  );

  if (isLoading || !data) {
    return (
      <div className="flex flex-col gap-5">
        <PageHeader description={translate("description")} title={translate("title")} />
        <Skeleton className="h-96 w-full" />
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />
      <ThemeForm editable={editable} onSaved={() => mutate()} settings={data} />
      <BrandAssets editable={editable} />
    </div>
  );
}

function ThemeForm({
  editable,
  onSaved,
  settings,
}: {
  editable: boolean;
  onSaved: () => void;
  settings: ThemeSettings;
}) {
  const translate = useTranslations("admin.branding");
  const { run, pending } = useOperatorAction();

  const [light, setLight] = useState<Palette>(settings.theme.light ?? {});
  const [dark, setDark] = useState<Palette>(settings.theme.dark ?? {});
  const [radius, setRadius] = useState(settings.theme.radius);
  const [density, setDensity] = useState(settings.theme.density);
  const [allowed, setAllowed] = useState<string[]>(settings.theme.allowedThemes);
  const [defaultTheme, setDefaultTheme] = useState(settings.theme.defaultTheme);
  const [dirty, setDirty] = useState(false);

  useUnsavedChanges(dirty, translate("unsaved"));

  function setColour(mode: (typeof MODES)[number], token: string, value: string) {
    const apply = mode === "light" ? setLight : setDark;
    apply((current) => {
      const next = { ...current };
      // An empty field means "use the design's value", which is a deletion
      // rather than a colour. Storing "" would fail the parser on save and
      // would be a strange way to say "I changed my mind".
      if (value.trim() === "") {
        delete next[token];
      } else {
        next[token] = value.trim();
      }
      return next;
    });
    setDirty(true);
  }

  function toggleMode(mode: string, offered: boolean) {
    setAllowed((current) => {
      const next = offered ? [...current, mode] : current.filter((entry) => entry !== mode);
      // Removing the last mode would leave nothing to render. The switch simply
      // refuses rather than producing a state the API would reject anyway.
      return next.length === 0 ? current : next;
    });
    setDirty(true);
  }

  async function save() {
    const ok = await run("/v1/panel/settings/theme", {
      body: {
        theme: {
          allowedThemes: allowed,
          dark,
          defaultTheme,
          density,
          light,
          radius,
        },
        version: settings.version,
      },
      method: "PUT",
    });
    if (ok) {
      setDirty(false);
      onSaved();
      toast.success(translate("saved"));
    }
  }

  function reset() {
    setLight({});
    setDark({});
    setRadius("default");
    setDensity("default");
    setAllowed(["light", "dark"]);
    setDefaultTheme("system");
    setDirty(true);
  }

  const blocking = settings.warnings.filter((warning) => warning.blocking);
  const advisory = settings.warnings.filter((warning) => !warning.blocking);

  return (
    <>
      {settings.warnings.length > 0 ? (
        <Alert variant={blocking.length > 0 ? "danger" : "warning"}>
          <AlertTitle>
            {blocking.length > 0 ? translate("warnings.blocking") : translate("warnings.advisory")}
          </AlertTitle>
          <AlertDescription>
            <ul className="space-y-1">
              {[...blocking, ...advisory].map((warning) => (
                <li key={`${warning.mode}-${warning.foreground}-${warning.background}`}>
                  {translate("warnings.pair", {
                    background: warning.background,
                    foreground: warning.foreground,
                    mode: translate(`mode.${warning.mode}`),
                    ratio: warning.ratio.toFixed(2),
                  })}
                </li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      ) : null}

      <Card>
        <CardHeader>
          <CardTitle>{translate("shape.title")}</CardTitle>
          <CardDescription>{translate("shape.description")}</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <Choice
            disabled={!editable}
            label={translate("shape.radius")}
            onChange={(value) => {
              setRadius(value);
              setDirty(true);
            }}
            options={RADII.map((value) => ({ label: translate(`radius.${value}`), value }))}
            value={radius}
          />
          <Choice
            disabled={!editable}
            label={translate("shape.density")}
            onChange={(value) => {
              setDensity(value);
              setDirty(true);
            }}
            options={DENSITIES.map((value) => ({ label: translate(`density.${value}`), value }))}
            value={density}
          />

          <fieldset className="flex flex-col gap-3">
            <legend className="font-medium text-sm">{translate("shape.offered")}</legend>
            <p className="text-subtle-foreground text-xs">{translate("shape.offeredHint")}</p>
            {MODES.map((mode) => (
              <ModeSwitch
                checked={allowed.includes(mode)}
                disabled={!editable}
                key={mode}
                label={translate(`mode.${mode}`)}
                onChange={(offered) => toggleMode(mode, offered)}
              />
            ))}
          </fieldset>

          <Choice
            disabled={!editable}
            label={translate("shape.default")}
            onChange={(value) => {
              setDefaultTheme(value);
              setDirty(true);
            }}
            options={["system", ...allowed].map((value) => ({
              label: translate(`mode.${value}`),
              value,
            }))}
            value={
              allowed.includes(defaultTheme) || defaultTheme === "system" ? defaultTheme : "system"
            }
          />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{translate("palette.title")}</CardTitle>
          <CardDescription>{translate("palette.description")}</CardDescription>
        </CardHeader>
        <CardContent>
          <Tabs defaultValue="light">
            <TabsList>
              {MODES.map((mode) => (
                <TabsTrigger key={mode} value={mode}>
                  {translate(`mode.${mode}`)}
                </TabsTrigger>
              ))}
            </TabsList>
            {MODES.map((mode) => (
              <TabsContent className="pt-4" key={mode} value={mode}>
                <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_20rem]">
                  <div className="grid gap-3 sm:grid-cols-2">
                    {settings.themable.map((token) => (
                      <ColourField
                        disabled={!editable}
                        key={token}
                        onChange={(value) => setColour(mode, token, value)}
                        token={token}
                        value={(mode === "light" ? light : dark)[token] ?? ""}
                      />
                    ))}
                  </div>
                  <Preview mode={mode} palette={mode === "light" ? light : dark} radius={radius} />
                </div>
              </TabsContent>
            ))}
          </Tabs>
        </CardContent>
      </Card>

      {editable ? (
        <div className="flex flex-wrap items-center gap-3">
          <Button disabled={pending} onClick={save}>
            {translate("save")}
          </Button>
          <Button disabled={pending} onClick={reset} variant="ghost">
            {translate("reset")}
          </Button>
          <span className="text-subtle-foreground text-xs">{translate("resetHint")}</span>
        </div>
      ) : null}
    </>
  );
}

function Choice({
  disabled,
  label,
  onChange,
  options,
  value,
}: {
  disabled: boolean;
  label: string;
  onChange: (value: string) => void;
  options: { label: string; value: string }[];
  value: string;
}) {
  const id = useId();
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor={id}>{label}</Label>
      <Select disabled={disabled} onValueChange={onChange} value={value}>
        <SelectTrigger id={id}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map((option) => (
            <SelectItem key={option.value} value={option.value}>
              {option.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

function ModeSwitch({
  checked,
  disabled,
  label,
  onChange,
}: {
  checked: boolean;
  disabled: boolean;
  label: string;
  onChange: (checked: boolean) => void;
}) {
  const id = useId();
  return (
    <div className="flex items-center gap-3">
      <Switch checked={checked} disabled={disabled} id={id} onCheckedChange={onChange} />
      <Label htmlFor={id}>{label}</Label>
    </div>
  );
}

/**
 * One token.
 *
 * The hex field is the control rather than a browser colour picker. A native
 * picker renders in the operating system's own chrome, ignores every design
 * token, and behaves differently on each platform — which is the thing a shared
 * component system exists to prevent, and the same reason this repository uses
 * no native select or date input. An operator branding an installation is
 * working from a hex value in a brand guide anyway; the swatch beside the field
 * is what turns that value back into a colour they can see.
 */
function ColourField({
  disabled,
  onChange,
  token,
  value,
}: {
  disabled: boolean;
  onChange: (value: string) => void;
  token: string;
  value: string;
}) {
  const id = useId();
  const valid = /^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/.test(value.trim());
  return (
    <div className="flex flex-col gap-1.5">
      <Label className="font-mono text-[11px]" htmlFor={id}>
        --{token}
      </Label>
      <div className="flex items-center gap-2">
        <span
          aria-hidden
          className="size-8 shrink-0 rounded-sm border border-border"
          style={valid ? { backgroundColor: value.trim() } : undefined}
        />
        <Input
          aria-invalid={value.trim() !== "" && !valid}
          className="font-mono"
          disabled={disabled}
          id={id}
          onChange={(event) => onChange(event.target.value)}
          placeholder="#000000"
          spellCheck={false}
          value={value}
        />
      </div>
    </div>
  );
}

/**
 * The palette on real components, before it is stored.
 *
 * Every token is a CSS custom property, so declaring the form's current values
 * on this one element is enough for everything inside it to render under them.
 * It is why the screen needs no colour maths of its own: the browser already
 * knows what these values look like together.
 */
function Preview({ mode, palette, radius }: { mode: string; palette: Palette; radius: string }) {
  const translate = useTranslations("admin.branding");
  const style: CSSProperties = {};
  for (const [token, colour] of Object.entries(palette)) {
    if (/^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/.test(colour.trim())) {
      (style as Record<string, string>)[`--${token}`] = colour.trim();
    }
  }
  (style as Record<string, string>)["--radius-scale"] = String(
    { compact: 0.6, default: 1, rounded: 1.6, square: 0 }[radius] ?? 1,
  );

  return (
    <div
      className={`${mode === "dark" ? "dark" : ""} rounded-lg border border-border bg-background p-4`}
      style={style}
    >
      <p className="font-mono text-[11px] text-subtle-foreground">{translate("preview.label")}</p>
      <div className="mt-3 rounded-lg border border-border bg-card p-3">
        <p className="font-semibold text-card-foreground text-sm">{translate("preview.heading")}</p>
        <p className="mt-1 text-muted-foreground text-xs">{translate("preview.body")}</p>
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <span className="rounded-md bg-primary px-3 py-1.5 font-medium text-primary-foreground text-xs">
            {translate("preview.primary")}
          </span>
          <span className="rounded-md bg-secondary px-3 py-1.5 font-medium text-secondary-foreground text-xs">
            {translate("preview.secondary")}
          </span>
          <Badge variant="neutral">{translate("preview.badge")}</Badge>
        </div>
      </div>
    </div>
  );
}

/** The three image slots. */
function BrandAssets({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.branding");
  const { data, mutate } = useSWR<BrandingAssets, ApiError>(
    "/v1/panel/settings/theme/assets",
    fetcher,
  );

  if (!data) {
    return <Skeleton className="h-48 w-full" />;
  }
  const stored = new Map((data.items ?? []).map((asset) => [asset.kind, asset]));

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("assets.title")}</CardTitle>
        <CardDescription>
          {translate("assets.description", {
            kilobytes: Math.round(data.maxBytes / 1024),
            types: data.contentTypes.map((type) => type.replace("image/", "")).join(", "),
          })}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4 sm:grid-cols-3">
        {data.kinds.map((kind) => (
          <AssetSlot
            accept={data.contentTypes.join(",")}
            asset={stored.get(kind)}
            editable={editable}
            key={kind}
            kind={kind}
            maxBytes={data.maxBytes}
            onChanged={() => mutate()}
          />
        ))}
      </CardContent>
    </Card>
  );
}

function AssetSlot({
  accept,
  asset,
  editable,
  kind,
  maxBytes,
  onChanged,
}: {
  accept: string;
  asset?: { checksum: string; byteSize: number };
  editable: boolean;
  kind: string;
  maxBytes: number;
  onChanged: () => void;
}) {
  const translate = useTranslations("admin.branding");
  const input = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);

  async function upload(file: File) {
    // The size is checked here as well as in the API, so a mistaken upload is
    // refused before a quarter of a megabyte crosses the network.
    if (file.size > maxBytes) {
      toast.error(translate("assets.tooLarge", { kilobytes: Math.round(maxBytes / 1024) }));
      return;
    }
    setBusy(true);
    try {
      // The body is the file itself. A multipart form would have to be buffered
      // before its size could be checked, and a base64 field would inflate the
      // bytes by a third inside a document that then has to be parsed.
      await apiFetch(`/v1/panel/settings/theme/assets/${kind}`, {
        body: file,
        headers: { "Content-Type": file.type },
        method: "PUT",
      });
      onChanged();
      toast.success(translate("assets.uploaded"));
    } catch (caught) {
      toast.error((caught as ApiError).message);
    } finally {
      setBusy(false);
      if (input.current) {
        input.current.value = "";
      }
    }
  }

  async function remove() {
    setBusy(true);
    try {
      await apiFetch(`/v1/panel/settings/theme/assets/${kind}`, { method: "DELETE" });
      onChanged();
      toast.success(translate("assets.removed"));
    } catch (caught) {
      toast.error((caught as ApiError).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex flex-col gap-2 rounded-lg border border-border p-3">
      <p className="font-medium text-sm">{translate(`assets.kind.${kind}`)}</p>
      {asset ? (
        <>
          {/* The address carries the checksum, so replacing an image shows the
              new one immediately rather than whatever the browser cached. */}
          {/* biome-ignore lint/performance/noImgElement: an operator-supplied asset served by checksum, not a build-time image */}
          <img
            alt={translate(`assets.kind.${kind}`)}
            className="h-16 w-auto self-start object-contain"
            src={`/v1/branding/assets/${kind}?v=${asset.checksum}`}
          />
          <p className="font-mono text-[11px] text-subtle-foreground">
            {Math.round(asset.byteSize / 1024)} KB
          </p>
        </>
      ) : (
        <p className="text-subtle-foreground text-xs">{translate("assets.empty")}</p>
      )}

      {editable ? (
        <div className="flex items-center gap-2">
          <input
            accept={accept}
            className="hidden"
            onChange={(event) => {
              const file = event.target.files?.[0];
              if (file) {
                void upload(file);
              }
            }}
            ref={input}
            type="file"
          />
          <Button
            disabled={busy}
            onClick={() => input.current?.click()}
            size="sm"
            variant="secondary"
          >
            <Upload aria-hidden />
            {translate("assets.upload")}
          </Button>
          {asset ? (
            <Button disabled={busy} onClick={remove} size="sm" variant="ghost">
              <Trash2 aria-hidden />
              {translate("assets.remove")}
            </Button>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
