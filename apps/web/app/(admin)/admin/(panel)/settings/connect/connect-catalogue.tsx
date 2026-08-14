"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { toast } from "@omniflow/ui/toast";
import { Plus, Trash2 } from "lucide-react";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { PageHeader } from "@/components/admin/resource-table";
import type { ApiError } from "@/lib/api";
import { fetcher } from "@/lib/api";
import type { ConnectCatalogue, ConnectClient, ConnectPlatform } from "@/lib/operations";
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
      <PlatformCard editable={editable} onChanged={() => mutate()} platforms={data.platforms} />
      {data.platforms.map((platform) => (
        <ClientCard
          clients={data.clients.filter((client) => client.platform === platform.slug)}
          editable={editable}
          key={platform.slug}
          onChanged={() => mutate()}
          platform={platform}
        />
      ))}
    </div>
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
    if (await run(`${ENDPOINT}/platforms`, { body: platform, method: "PUT" })) {
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
    <div className="grid gap-3 rounded-lg border border-border p-3 sm:grid-cols-[10rem_1fr_1fr_5rem_auto]">
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
      <div className="flex items-end gap-2">
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
            {isNew ? (
              <Button onClick={onRemove} size="sm" variant="ghost">
                {translate("cancel")}
              </Button>
            ) : (
              <>
                <Button onClick={() => setConfirming(true)} size="sm" variant="ghost">
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
  editable,
  onChanged,
  platform,
}: {
  clients: ConnectClient[];
  editable: boolean;
  onChanged: () => void;
  platform: ConnectPlatform;
}) {
  const translate = useTranslations("admin.connect");
  const { run, pending } = useOperatorAction();
  const [draft, setDraft] = useState<ConnectClient | null>(null);

  async function save(client: ConnectClient) {
    if (await run(`${ENDPOINT}/clients`, { body: client, method: "PUT" })) {
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
          />
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
  client,
  editable,
  isNew,
  onRemove,
  onSave,
  pending,
}: {
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
    <div className="flex flex-col gap-3 rounded-lg border border-border p-3">
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
            <Button disabled={pending} onClick={onRemove} size="sm" variant="ghost">
              {isNew ? translate("cancel") : <Trash2 aria-hidden />}
            </Button>
          </>
        ) : null}
      </div>
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
