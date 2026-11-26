"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { toast } from "@omniflow/ui/toast";
import { Plus, RefreshCw, Trash2 } from "lucide-react";
import { useFormatter, useTranslations } from "next-intl";
import { type ReactNode, useId, useState } from "react";
import useSWR from "swr";

import { PageHeader } from "@/components/admin/resource-table";
import type { ApiError } from "@/lib/api";
import { apiFetch, fetcher } from "@/lib/api";
import { useBytes } from "@/lib/format";
import type {
  ConnectCatalogue,
  ConnectClient,
  ConnectDownload,
  ConnectPlatform,
} from "@/lib/operations";
import { useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

const ENDPOINT = "/v1/panel/settings/connect";

/**
 * The connection guidance an installation gives its customers.
 *
 * This was a table compiled into the Go binary, which guaranteed that the bot
 * and the browser could never recommend different applications and made adding
 * one a release. It is now rows, read by both surfaces through one query — the
 * same guarantee, without the release.
 *
 * Two fields carry rules rather than preferences, and both are enforced by the
 * API rather than here. An import scheme ends up in the href of a link on a page
 * that holds a session cookie, so it must look like `happ://add/` and can never
 * name javascript, data, vbscript, or file. A download address must be https,
 * because it is handed to somebody about to install software on their own
 * device.
 */
export function ConnectCatalogueScreen() {
  const translate = useTranslations("admin.connect");
  const { can } = useSession();
  const editable = can("settings.write");

  const { data, isLoading, mutate } = useSWR<ConnectCatalogue, ApiError>(ENDPOINT, fetcher);

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
      {editable ? <RemnawaveImportCard onImported={() => mutate()} /> : null}
      <PlatformCard editable={editable} onChanged={() => mutate()} platforms={data.platforms} />
      {data.platforms.map((platform) => (
        <ClientCard
          clients={data.clients.filter((client) => client.platform === platform.slug)}
          downloads={data.downloads}
          editable={editable}
          key={platform.slug}
          maxFileBytes={data.maxFileBytes}
          mirroring={data.mirroring}
          onChanged={() => mutate()}
          platform={platform}
        />
      ))}
    </div>
  );
}

/**
 * A row without the fields the API writes itself. A draft carries an empty
 * `updatedAt`, which the API's strict decoder refuses as a timestamp, and the
 * audit fields are the server's to set either way.
 */
function writable<Row extends { updatedAt?: string; updatedBy?: string }>(
  row: Row,
): Omit<Row, "updatedAt" | "updatedBy"> {
  const { updatedAt: _updatedAt, updatedBy: _updatedBy, ...rest } = row;
  return rest;
}

/** A download as an operator edits it: the source, never what the worker recorded. */
function writableDownload(download: ConnectDownload) {
  return {
    assetPattern: download.kind === "mirror" ? download.assetPattern : undefined,
    clientId: download.clientId,
    enabled: download.enabled,
    id: download.id,
    kind: download.kind,
    label: download.label,
    sortOrder: download.sortOrder,
    sourceRepo: download.kind === "mirror" ? download.sourceRepo : undefined,
    tagPrefix: download.kind === "mirror" ? download.tagPrefix : undefined,
    url: download.kind === "store" ? download.url : undefined,
  };
}

type ImportResult = { platforms: number; clients: number; skipped: string[] };

/**
 * Copies the applications of a Remnawave subscription page into this
 * catalogue. Platforms are matched by key and applications by platform and
 * name, so importing again updates rather than duplicates; entries with other
 * names are left alone.
 */
function RemnawaveImportCard({ onImported }: { onImported: () => void }) {
  const translate = useTranslations("admin.connect.import");
  const inputId = useId();
  const [configUuid, setConfigUuid] = useState("");
  const [pending, setPending] = useState(false);
  const [result, setResult] = useState<ImportResult | null>(null);

  async function importPage() {
    setPending(true);
    try {
      const imported = await apiFetch<ImportResult>(`${ENDPOINT}/import-remnawave`, {
        body: JSON.stringify({ configUuid: configUuid.trim() }),
        method: "POST",
      });
      setResult(imported);
      onImported();
      toast.success(
        translate("done", { clients: imported.clients, platforms: imported.platforms }),
      );
    } catch (importError) {
      toast.error((importError as ApiError).message);
    } finally {
      setPending(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={inputId}>{translate("uuid")}</Label>
          <div className="flex flex-col gap-2 sm:flex-row">
            <Input
              autoComplete="off"
              className="font-mono"
              id={inputId}
              onChange={(event) => setConfigUuid(event.target.value)}
              placeholder="00000000-0000-0000-0000-000000000000"
              value={configUuid}
            />
            <Button
              disabled={pending || configUuid.trim().length !== 36}
              onClick={importPage}
              variant="outline"
            >
              {pending ? translate("importing") : translate("action")}
            </Button>
          </div>
        </div>
        {result && result.skipped.length > 0 ? (
          <p className="text-[12.5px] text-muted-foreground">
            {translate("skipped", { names: result.skipped.join(", ") })}
          </p>
        ) : null}
      </CardContent>
    </Card>
  );
}

function PlatformCard({
  editable,
  onChanged,
  platforms,
}: {
  editable: boolean;
  onChanged: () => void;
  platforms: ConnectPlatform[];
}) {
  const translate = useTranslations("admin.connect");
  const { run, pending } = useOperatorAction();
  const [draft, setDraft] = useState<ConnectPlatform | null>(null);

  async function save(platform: ConnectPlatform) {
    if (await run(`${ENDPOINT}/platforms`, { body: writable(platform), method: "PUT" })) {
      setDraft(null);
      onChanged();
      toast.success(translate("saved"));
    }
  }

  async function remove(slug: string) {
    if (await run(`${ENDPOINT}/platforms/${encodeURIComponent(slug)}`, { method: "DELETE" })) {
      onChanged();
      toast.success(translate("platformRemoved"));
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("platforms.title")}</CardTitle>
        <CardDescription>{translate("platforms.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {platforms.map((platform) => (
          <PlatformRow
            editable={editable}
            key={platform.slug}
            onRemove={() => remove(platform.slug)}
            onSave={save}
            pending={pending}
            platform={platform}
          />
        ))}
        {platforms.length === 0 ? (
          <p className="text-subtle-foreground text-sm">{translate("platforms.empty")}</p>
        ) : null}

        {draft ? (
          <PlatformRow
            editable={editable}
            isNew
            onRemove={() => setDraft(null)}
            onSave={save}
            pending={pending}
            platform={draft}
          />
        ) : null}
        {editable && !draft ? (
          <Button
            className="self-start"
            onClick={() =>
              setDraft({
                enabled: true,
                labelEn: "",
                labelRu: "",
                slug: "",
                sortOrder: (platforms.at(-1)?.sortOrder ?? 0) + 10,
                updatedAt: "",
              })
            }
            size="sm"
            variant="secondary"
          >
            <Plus aria-hidden />
            {translate("platforms.add")}
          </Button>
        ) : null}
      </CardContent>
    </Card>
  );
}

function PlatformRow({
  editable,
  isNew,
  onRemove,
  onSave,
  pending,
  platform,
}: {
  editable: boolean;
  isNew?: boolean;
  onRemove: () => void;
  onSave: (platform: ConnectPlatform) => void;
  pending: boolean;
  platform: ConnectPlatform;
}) {
  const translate = useTranslations("admin.connect");
  const [form, setForm] = useState(platform);
  const [confirming, setConfirming] = useState(false);
  const slugId = useId();
  const labelEnId = useId();
  const labelRuId = useId();
  const orderId = useId();

  return (
    <div className="flex flex-col gap-3 rounded-lg border bg-well border-border p-3">
      <EnabledSwitch
        checked={form.enabled}
        disabled={!editable}
        onChange={(enabled) => setForm({ ...form, enabled })}
      />
      <div className="grid items-end gap-3 sm:grid-cols-[10rem_1fr_1fr_5rem]">
        <Field
          disabled={!editable || !isNew}
          id={slugId}
          label={translate("platforms.slug")}
          onChange={(slug) => setForm({ ...form, slug })}
          value={form.slug}
        />
        <Field
          disabled={!editable}
          id={labelEnId}
          label={translate("platforms.labelEn")}
          onChange={(labelEn) => setForm({ ...form, labelEn })}
          value={form.labelEn}
        />
        <Field
          disabled={!editable}
          id={labelRuId}
          label={translate("platforms.labelRu")}
          onChange={(labelRu) => setForm({ ...form, labelRu })}
          value={form.labelRu}
        />
        <Field
          disabled={!editable}
          id={orderId}
          label={translate("order")}
          onChange={(value) => setForm({ ...form, sortOrder: Number(value) || 0 })}
          value={String(form.sortOrder)}
        />
      </div>
      <div className="flex items-center gap-2">
        {editable ? (
          <>
            <Button disabled={pending} onClick={() => onSave(form)} size="sm">
              {translate("save")}
            </Button>
            {isNew ? (
              <Button onClick={onRemove} size="sm" variant="ghost">
                {translate("cancel")}
              </Button>
            ) : (
              <>
                <Button
                  aria-label={translate("delete")}
                  onClick={() => setConfirming(true)}
                  size="sm"
                  variant="ghost"
                >
                  <Trash2 aria-hidden />
                </Button>
                {/* Deleting a platform takes its clients with it, which is why
                    this confirms and the switch beside it does not. */}
                <ConfirmDialog
                  cancelLabel={translate("cancel")}
                  confirmLabel={translate("delete")}
                  description={translate("platforms.deleteWarning")}
                  destructive
                  onConfirm={() => {
                    setConfirming(false);
                    onRemove();
                  }}
                  onOpenChange={setConfirming}
                  open={confirming}
                  title={translate("platforms.deleteTitle", {
                    platform: form.labelEn || form.slug,
                  })}
                />
              </>
            )}
          </>
        ) : null}
      </div>
    </div>
  );
}

function ClientCard({
  clients,
  downloads,
  editable,
  maxFileBytes,
  mirroring,
  onChanged,
  platform,
}: {
  clients: ConnectClient[];
  downloads: ConnectDownload[];
  editable: boolean;
  maxFileBytes: number;
  mirroring: boolean;
  onChanged: () => void;
  platform: ConnectPlatform;
}) {
  const translate = useTranslations("admin.connect");
  const { run, pending } = useOperatorAction();
  const [draft, setDraft] = useState<ConnectClient | null>(null);

  async function save(client: ConnectClient) {
    if (await run(`${ENDPOINT}/clients`, { body: writable(client), method: "PUT" })) {
      setDraft(null);
      onChanged();
      toast.success(translate("saved"));
    }
  }

  async function remove(id: string) {
    if (await run(`${ENDPOINT}/clients/${id}`, { method: "DELETE" })) {
      onChanged();
      toast.success(translate("clientRemoved"));
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{platform.labelEn || platform.slug}</CardTitle>
        <CardDescription>{translate("clients.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {clients.map((client) => (
          <ClientRow
            client={client}
            editable={editable}
            key={client.id}
            onRemove={() => remove(client.id ?? "")}
            onSave={save}
            pending={pending}
          >
            <DownloadList
              clientId={client.id ?? ""}
              downloads={downloads.filter((download) => download.clientId === client.id)}
              editable={editable}
              maxFileBytes={maxFileBytes}
              mirroring={mirroring}
              onChanged={onChanged}
            />
          </ClientRow>
        ))}
        {clients.length === 0 && !draft ? (
          <p className="text-subtle-foreground text-sm">{translate("clients.empty")}</p>
        ) : null}

        {draft ? (
          <ClientRow
            client={draft}
            editable={editable}
            isNew
            onRemove={() => setDraft(null)}
            onSave={save}
            pending={pending}
          />
        ) : null}
        {editable && !draft ? (
          <Button
            className="self-start"
            onClick={() =>
              setDraft({
                enabled: true,
                name: "",
                platform: platform.slug,
                scheme: "",
                sortOrder: (clients.at(-1)?.sortOrder ?? 0) + 10,
                updatedAt: "",
              })
            }
            size="sm"
            variant="secondary"
          >
            <Plus aria-hidden />
            {translate("clients.add")}
          </Button>
        ) : null}
      </CardContent>
    </Card>
  );
}

function ClientRow({
  children,
  client,
  editable,
  isNew,
  onRemove,
  onSave,
  pending,
}: {
  children?: ReactNode;
  client: ConnectClient;
  editable: boolean;
  isNew?: boolean;
  onRemove: () => void;
  onSave: (client: ConnectClient) => void;
  pending: boolean;
}) {
  const translate = useTranslations("admin.connect");
  const [form, setForm] = useState(client);
  const nameId = useId();
  const schemeId = useId();
  const downloadId = useId();
  const orderId = useId();
  const instructionsEnId = useId();
  const instructionsRuId = useId();

  return (
    <div className="flex flex-col gap-3 rounded-lg border bg-well border-border p-3">
      <div className="grid gap-3 sm:grid-cols-[1fr_1fr_5rem]">
        <Field
          disabled={!editable}
          id={nameId}
          label={translate("clients.name")}
          onChange={(name) => setForm({ ...form, name })}
          value={form.name}
        />
        <Field
          disabled={!editable}
          hint={translate("clients.schemeHint")}
          id={schemeId}
          label={translate("clients.scheme")}
          mono
          onChange={(scheme) => setForm({ ...form, scheme })}
          value={form.scheme}
        />
        <Field
          disabled={!editable}
          id={orderId}
          label={translate("order")}
          onChange={(value) => setForm({ ...form, sortOrder: Number(value) || 0 })}
          value={String(form.sortOrder)}
        />
      </div>
      <Field
        disabled={!editable}
        hint={translate("clients.downloadHint")}
        id={downloadId}
        label={translate("clients.download")}
        mono
        onChange={(downloadUrl) => setForm({ ...form, downloadUrl })}
        value={form.downloadUrl ?? ""}
      />
      <div className="grid gap-3 sm:grid-cols-2">
        <Field
          disabled={!editable}
          hint={translate("clients.instructionsHint")}
          id={instructionsEnId}
          label={translate("clients.instructionsEn")}
          onChange={(instructionsEn) => setForm({ ...form, instructionsEn })}
          value={form.instructionsEn ?? ""}
        />
        <Field
          disabled={!editable}
          id={instructionsRuId}
          label={translate("clients.instructionsRu")}
          onChange={(instructionsRu) => setForm({ ...form, instructionsRu })}
          value={form.instructionsRu ?? ""}
        />
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <EnabledSwitch
          checked={form.enabled}
          disabled={!editable}
          onChange={(enabled) => setForm({ ...form, enabled })}
        />
        {editable ? (
          <>
            <Button disabled={pending} onClick={() => onSave(form)} size="sm">
              {translate("save")}
            </Button>
            <Button
              aria-label={isNew ? undefined : translate("delete")}
              disabled={pending}
              onClick={onRemove}
              size="sm"
              variant="ghost"
            >
              {isNew ? translate("cancel") : <Trash2 aria-hidden />}
            </Button>
          </>
        ) : null}
      </div>
      {children}
    </div>
  );
}

/**
 * Where a customer gets one application: store links, and installers the
 * worker copies from GitHub releases into the object store and the API serves
 * from this installation's own address.
 *
 * A mirror is checked every six hours and whenever its source is saved or
 * "check now" is pressed; the worker looks for due mirrors every minute, so
 * nothing here waits on a 150 MB download.
 */
function DownloadList({
  clientId,
  downloads,
  editable,
  maxFileBytes,
  mirroring,
  onChanged,
}: {
  clientId: string;
  downloads: ConnectDownload[];
  editable: boolean;
  maxFileBytes: number;
  mirroring: boolean;
  onChanged: () => void;
}) {
  const translate = useTranslations("admin.connect.downloads");
  const translateConnect = useTranslations("admin.connect");
  const bytes = useBytes();
  const { run, pending } = useOperatorAction();
  const [draft, setDraft] = useState<ConnectDownload | null>(null);

  async function save(download: ConnectDownload) {
    if (await run(`${ENDPOINT}/downloads`, { body: writableDownload(download), method: "PUT" })) {
      setDraft(null);
      onChanged();
      toast.success(translateConnect("saved"));
    }
  }

  async function remove(id: string) {
    if (await run(`${ENDPOINT}/downloads/${id}`, { method: "DELETE" })) {
      onChanged();
      toast.success(translate("removed"));
    }
  }

  async function check(id: string) {
    if (await run(`${ENDPOINT}/downloads/${id}/check`, { method: "POST" })) {
      onChanged();
      toast.success(translate("checkQueued"));
    }
  }

  return (
    <div className="flex flex-col gap-3 border-border border-t pt-3">
      <div className="flex flex-col gap-1">
        <p className="font-medium text-sm">{translate("title")}</p>
        <p className="text-subtle-foreground text-xs">
          {translate("description")} {translate("maxSize", { size: bytes(maxFileBytes) })}
        </p>
        {mirroring ? null : <p className="text-warning text-xs">{translate("noStore")}</p>}
      </div>
      {downloads.map((download) => (
        <DownloadRow
          download={download}
          editable={editable}
          key={download.id}
          onCheck={() => check(download.id ?? "")}
          onRemove={() => remove(download.id ?? "")}
          onSave={save}
          pending={pending}
        />
      ))}
      {downloads.length === 0 && !draft ? (
        <p className="text-subtle-foreground text-xs">{translate("empty")}</p>
      ) : null}
      {draft ? (
        <DownloadRow
          download={draft}
          editable={editable}
          isNew
          onRemove={() => setDraft(null)}
          onSave={save}
          pending={pending}
        />
      ) : null}
      {editable && !draft && clientId ? (
        <Button
          className="self-start"
          onClick={() =>
            setDraft({
              clientId,
              enabled: true,
              kind: "mirror",
              label: "",
              sortOrder: (downloads.at(-1)?.sortOrder ?? 0) + 10,
              updatedAt: "",
            })
          }
          size="sm"
          variant="outline"
        >
          <Plus aria-hidden />
          {translate("add")}
        </Button>
      ) : null}
    </div>
  );
}

function DownloadRow({
  download,
  editable,
  isNew,
  onCheck,
  onRemove,
  onSave,
  pending,
}: {
  download: ConnectDownload;
  editable: boolean;
  isNew?: boolean;
  onCheck?: () => void;
  onRemove: () => void;
  onSave: (download: ConnectDownload) => void;
  pending: boolean;
}) {
  const translate = useTranslations("admin.connect.downloads");
  const translateConnect = useTranslations("admin.connect");
  const [form, setForm] = useState(download);
  const kindId = useId();
  const labelId = useId();
  const orderId = useId();
  const urlId = useId();
  const repoId = useId();
  const prefixId = useId();
  const assetId = useId();
  const mirror = form.kind === "mirror";

  return (
    <div className="flex flex-col gap-3 rounded-md border border-border bg-card p-3">
      <div className="grid gap-3 sm:grid-cols-[12rem_1fr_5rem]">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={kindId}>{translate("kind")}</Label>
          <Select
            disabled={!editable}
            onValueChange={(kind) => setForm({ ...form, kind: kind as ConnectDownload["kind"] })}
            value={form.kind}
          >
            <SelectTrigger id={kindId}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="mirror">{translate("kindMirror")}</SelectItem>
              <SelectItem value="store">{translate("kindStore")}</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <Field
          disabled={!editable}
          hint={translate("labelHint")}
          id={labelId}
          label={translate("label")}
          onChange={(label) => setForm({ ...form, label })}
          value={form.label}
        />
        <Field
          disabled={!editable}
          id={orderId}
          label={translateConnect("order")}
          onChange={(value) => setForm({ ...form, sortOrder: Number(value) || 0 })}
          value={String(form.sortOrder)}
        />
      </div>
      {mirror ? (
        <div className="grid gap-3 sm:grid-cols-[1fr_10rem_1fr]">
          <Field
            disabled={!editable}
            hint={translate("repoHint")}
            id={repoId}
            label={translate("repo")}
            mono
            onChange={(sourceRepo) => setForm({ ...form, sourceRepo })}
            value={form.sourceRepo ?? ""}
          />
          <Field
            disabled={!editable}
            hint={translate("tagPrefixHint")}
            id={prefixId}
            label={translate("tagPrefix")}
            mono
            onChange={(tagPrefix) => setForm({ ...form, tagPrefix })}
            value={form.tagPrefix ?? ""}
          />
          <Field
            disabled={!editable}
            hint={translate("assetHint")}
            id={assetId}
            label={translate("asset")}
            mono
            onChange={(assetPattern) => setForm({ ...form, assetPattern })}
            value={form.assetPattern ?? ""}
          />
        </div>
      ) : (
        <Field
          disabled={!editable}
          hint={translate("urlHint")}
          id={urlId}
          label={translate("url")}
          mono
          onChange={(url) => setForm({ ...form, url })}
          value={form.url ?? ""}
        />
      )}
      {mirror && !isNew && download.kind === "mirror" ? <MirrorStatus download={download} /> : null}
      <div className="flex flex-wrap items-center gap-2">
        <EnabledSwitch
          checked={form.enabled}
          disabled={!editable}
          onChange={(enabled) => setForm({ ...form, enabled })}
        />
        {editable ? (
          <>
            <Button disabled={pending} onClick={() => onSave(form)} size="sm">
              {translateConnect("save")}
            </Button>
            {!isNew && download.kind === "mirror" && onCheck ? (
              <Button disabled={pending} onClick={onCheck} size="sm" variant="outline">
                <RefreshCw aria-hidden />
                {translate("check")}
              </Button>
            ) : null}
            <Button
              aria-label={isNew ? undefined : translateConnect("delete")}
              disabled={pending}
              onClick={onRemove}
              size="sm"
              variant="ghost"
            >
              {isNew ? translateConnect("cancel") : <Trash2 aria-hidden />}
            </Button>
          </>
        ) : null}
      </div>
    </div>
  );
}

/** What a mirror holds, and how its last check went. */
function MirrorStatus({ download }: { download: ConnectDownload }) {
  const translate = useTranslations("admin.connect.downloads");
  const format = useFormatter();
  const bytes = useBytes();
  const when = (value?: string) =>
    value ? format.dateTime(new Date(value), { dateStyle: "short", timeStyle: "short" }) : "";

  return (
    <div className="flex flex-col gap-0.5 text-xs">
      {download.fileName && download.path ? (
        <p>
          <a className="underline underline-offset-2" href={download.path}>
            {translate("holds", {
              fetched: when(download.fetchedAt),
              file: download.fileName,
              size: bytes(download.sizeBytes ?? 0),
              version: download.version ?? "",
            })}
          </a>
        </p>
      ) : (
        <p className="text-subtle-foreground">{translate("waiting")}</p>
      )}
      {download.lastError ? (
        <p className="text-destructive">{translate("failed", { error: download.lastError })}</p>
      ) : download.checkedAt ? (
        <p className="text-subtle-foreground">
          {translate("checked", { checked: when(download.checkedAt) })}
        </p>
      ) : null}
    </div>
  );
}

function Field({
  disabled,
  hint,
  id,
  label,
  mono,
  onChange,
  value,
}: {
  disabled: boolean;
  hint?: string;
  id: string;
  label: string;
  mono?: boolean;
  onChange: (value: string) => void;
  value: string;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Input
        className={mono ? "font-mono" : undefined}
        disabled={disabled}
        id={id}
        onChange={(event) => onChange(event.target.value)}
        spellCheck={false}
        value={value}
      />
      {hint ? <p className="text-subtle-foreground text-xs">{hint}</p> : null}
    </div>
  );
}

function EnabledSwitch({
  checked,
  disabled,
  onChange,
}: {
  checked: boolean;
  disabled: boolean;
  onChange: (checked: boolean) => void;
}) {
  const translate = useTranslations("admin.connect");
  const id = useId();
  return (
    <div className="flex items-center gap-2">
      <Switch checked={checked} disabled={disabled} id={id} onCheckedChange={onChange} />
      <Label htmlFor={id}>{translate("enabled")}</Label>
    </div>
  );
}
