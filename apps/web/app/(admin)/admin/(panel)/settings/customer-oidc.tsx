"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { EmptyState } from "@omniflow/ui/empty-state";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { KeyRound } from "lucide-react";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";
import { type Listing, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

/** Mirrors CustomerOidcProvider in api/openapi.yaml. */
type CustomerOidcProvider = {
  slug: string;
  displayName: string;
  issuer: string;
  discoveryUrl: string;
  clientId: string;
  scopes: string[] | null;
  enabled: boolean;
  icon?: string;
  sortOrder: number;
  requireVerifiedEmail: boolean;
  allowAutoProvision: boolean;
  hasClientSecret: boolean;
};

/** Mirrors an item of CustomerOidcPresetList. */
type CustomerOidcPreset = {
  slug: string;
  displayName: string;
  issuer: string;
  discoveryUrl: string;
  scopes: string[] | null;
  icon?: string;
  requireVerifiedEmail: boolean;
  note?: string;
};

/** The editable state of one provider, before it is sent. */
type Draft = {
  slug: string;
  displayName: string;
  issuer: string;
  discoveryUrl: string;
  clientId: string;
  clientSecret: string;
  scopes: string;
  enabled: boolean;
  icon: string;
  sortOrder: string;
  requireVerifiedEmail: boolean;
  allowAutoProvision: boolean;
};

function draftFromProvider(provider: CustomerOidcProvider): Draft {
  return {
    allowAutoProvision: provider.allowAutoProvision,
    // The secret is never sent back, so the field always starts empty. Leaving
    // it empty on save keeps the stored one.
    clientId: provider.clientId,
    clientSecret: "",
    discoveryUrl: provider.discoveryUrl,
    displayName: provider.displayName,
    enabled: provider.enabled,
    icon: provider.icon ?? "",
    issuer: provider.issuer,
    requireVerifiedEmail: provider.requireVerifiedEmail,
    scopes: (provider.scopes ?? []).join(" "),
    slug: provider.slug,
    sortOrder: String(provider.sortOrder),
  };
}

function draftFromPreset(preset: CustomerOidcPreset): Draft {
  return {
    // A preset is data, not a code path: it prefills exactly the fields an
    // operator could have typed, and nothing downstream branches on which one a
    // provider came from.
    allowAutoProvision: true,
    clientId: "",
    clientSecret: "",
    discoveryUrl: preset.discoveryUrl,
    displayName: preset.displayName,
    enabled: false,
    icon: preset.icon ?? "",
    issuer: preset.issuer,
    requireVerifiedEmail: preset.requireVerifiedEmail,
    scopes: (preset.scopes ?? []).join(" "),
    slug: preset.slug,
    sortOrder: "0",
  };
}

const EMPTY_DRAFT: Draft = {
  allowAutoProvision: true,
  clientId: "",
  clientSecret: "",
  discoveryUrl: "",
  displayName: "",
  enabled: false,
  icon: "",
  issuer: "",
  requireVerifiedEmail: true,
  scopes: "openid profile email",
  slug: "",
  sortOrder: "0",
};

/**
 * The customer panel's sign-in providers.
 *
 * v0.9 shipped this capability with an API and no form, so an operator
 * configured a provider by calling the endpoint directly. This is that form.
 *
 * Two properties of the underlying API shape the screen. The client secret is
 * write-only — the list reports only whether one is held — so the field is
 * always blank and an empty field means "keep what is stored" rather than
 * "clear it". And disabling a provider ends the sessions it established, which
 * is stated on the screen rather than discovered afterwards: an operator turning
 * one off during an incident needs to know that it also signs those customers
 * out.
 */
export function CustomerOidcSettings() {
  const translate = useTranslations("admin.customerOidc");
  const { can } = useSession();
  const editable = can("settings.write");

  const { data, isLoading, mutate } = useSWR<Listing<CustomerOidcProvider>, ApiError>(
    "/v1/panel/settings/customer-oidc",
    fetcher,
  );
  const { data: presets } = useSWR<Listing<CustomerOidcPreset>, ApiError>(
    "/v1/panel/settings/customer-oidc/presets",
    fetcher,
  );

  const [draft, setDraft] = useState<Draft | null>(null);
  const providers = data?.items ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {isLoading ? (
          <Skeleton className="h-40 w-full" />
        ) : providers.length === 0 ? (
          <EmptyState
            description={translate("emptyDescription")}
            icon={<KeyRound />}
            title={translate("empty")}
          />
        ) : (
          providers.map((provider) => (
            <ProviderForm
              editable={editable}
              initial={draftFromProvider(provider)}
              key={provider.slug}
              known
              onSaved={() => mutate()}
              secretConfigured={provider.hasClientSecret}
            />
          ))
        )}

        {editable && (
          <div className="flex flex-col gap-3 border-border border-t pt-4">
            <p className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
              {translate("addTitle")}
            </p>
            <p className="text-muted-foreground text-xs">{translate("addDescription")}</p>
            <div className="flex flex-wrap gap-2">
              {(presets?.items ?? []).map((preset) => (
                <Button
                  key={preset.slug}
                  onClick={() => setDraft(draftFromPreset(preset))}
                  size="sm"
                  type="button"
                  variant="outline"
                >
                  {preset.displayName}
                </Button>
              ))}
              <Button
                onClick={() => setDraft(EMPTY_DRAFT)}
                size="sm"
                type="button"
                variant="outline"
              >
                {translate("addBlank")}
              </Button>
            </div>
            {draft && (
              <ProviderForm
                editable
                initial={draft}
                known={false}
                onCancel={() => setDraft(null)}
                onSaved={() => {
                  setDraft(null);
                  mutate();
                }}
                secretConfigured={false}
              />
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

/**
 * One provider's fields.
 *
 * `known` distinguishes a provider that already exists from one being added.
 * The slug is the identity the API upserts on, so it is fixed once a provider
 * exists: editing it would silently create a second provider and leave the
 * first one enabled.
 */
function ProviderForm({
  editable,
  initial,
  known,
  onCancel,
  onSaved,
  secretConfigured,
}: {
  editable: boolean;
  initial: Draft;
  known: boolean;
  onCancel?: () => void;
  onSaved: () => void;
  secretConfigured: boolean;
}) {
  const translate = useTranslations("admin.customerOidc");
  const [draft, setDraft] = useState(initial);
  const [confirming, setConfirming] = useState(false);
  const { run, pending, error } = useOperatorAction();

  const slugId = useId();
  const nameId = useId();
  const issuerId = useId();
  const discoveryId = useId();
  const clientId = useId();
  const secretId = useId();
  const scopesId = useId();
  const iconId = useId();
  const orderId = useId();
  const enabledId = useId();
  const verifiedId = useId();
  const provisionId = useId();

  function set<Key extends keyof Draft>(key: Key, value: Draft[Key]) {
    setDraft((current) => ({ ...current, [key]: value }));
  }

  const complete =
    draft.slug.trim() !== "" &&
    draft.displayName.trim() !== "" &&
    draft.issuer.trim() !== "" &&
    draft.discoveryUrl.trim() !== "" &&
    draft.clientId.trim() !== "";

  async function save() {
    const ok = await run("/v1/panel/settings/customer-oidc", {
      body: {
        allowAutoProvision: draft.allowAutoProvision,
        clientId: draft.clientId.trim(),
        // Omitted rather than sent empty: the API treats an absent secret as
        // "keep the stored one", and sending "" would read as a new value.
        ...(draft.clientSecret === "" ? {} : { clientSecret: draft.clientSecret }),
        discoveryUrl: draft.discoveryUrl.trim(),
        displayName: draft.displayName.trim(),
        enabled: draft.enabled,
        icon: draft.icon.trim(),
        issuer: draft.issuer.trim(),
        requireVerifiedEmail: draft.requireVerifiedEmail,
        scopes: draft.scopes.split(/\s+/).filter(Boolean),
        slug: draft.slug.trim(),
        sortOrder: Number(draft.sortOrder) || 0,
      },
      method: "PUT",
    });
    if (ok) {
      setDraft((current) => ({ ...current, clientSecret: "" }));
      onSaved();
    }
  }

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border p-4">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium text-sm">{draft.displayName || translate("untitled")}</span>
        <Badge variant={draft.enabled ? "success" : "neutral"}>
          {translate(draft.enabled ? "enabled" : "disabled")}
        </Badge>
        {secretConfigured && <Badge variant="neutral">{translate("secretStored")}</Badge>}
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <Field id={slugId} label={translate("fields.slug")}>
          <Input
            disabled={known || !editable}
            id={slugId}
            onChange={(event) => set("slug", event.target.value)}
            value={draft.slug}
          />
        </Field>
        <Field id={nameId} label={translate("fields.displayName")}>
          <Input
            disabled={!editable}
            id={nameId}
            onChange={(event) => set("displayName", event.target.value)}
            value={draft.displayName}
          />
        </Field>
        <Field id={issuerId} label={translate("fields.issuer")}>
          <Input
            disabled={!editable}
            id={issuerId}
            inputMode="url"
            onChange={(event) => set("issuer", event.target.value)}
            value={draft.issuer}
          />
        </Field>
        <Field id={discoveryId} label={translate("fields.discoveryUrl")}>
          <Input
            disabled={!editable}
            id={discoveryId}
            inputMode="url"
            onChange={(event) => set("discoveryUrl", event.target.value)}
            value={draft.discoveryUrl}
          />
        </Field>
        <Field id={clientId} label={translate("fields.clientId")}>
          <Input
            disabled={!editable}
            id={clientId}
            onChange={(event) => set("clientId", event.target.value)}
            value={draft.clientId}
          />
        </Field>
        <Field
          hint={translate(secretConfigured ? "fields.secretKept" : "fields.secretHint")}
          id={secretId}
          label={translate("fields.clientSecret")}
        >
          <Input
            autoComplete="off"
            disabled={!editable}
            id={secretId}
            onChange={(event) => set("clientSecret", event.target.value)}
            type="password"
            value={draft.clientSecret}
          />
        </Field>
        <Field
          hint={translate("fields.scopesHint")}
          id={scopesId}
          label={translate("fields.scopes")}
        >
          <Input
            disabled={!editable}
            id={scopesId}
            onChange={(event) => set("scopes", event.target.value)}
            value={draft.scopes}
          />
        </Field>
        <Field hint={translate("fields.iconHint")} id={iconId} label={translate("fields.icon")}>
          <Input
            disabled={!editable}
            id={iconId}
            onChange={(event) => set("icon", event.target.value)}
            value={draft.icon}
          />
        </Field>
        <Field id={orderId} label={translate("fields.sortOrder")}>
          <Input
            disabled={!editable}
            id={orderId}
            inputMode="numeric"
            onChange={(event) => set("sortOrder", event.target.value)}
            value={draft.sortOrder}
          />
        </Field>
      </div>

      <div className="flex flex-col gap-2">
        <Toggle
          checked={draft.enabled}
          disabled={!editable}
          hint={translate("fields.enabledHint")}
          id={enabledId}
          label={translate("fields.enabled")}
          onChange={(value) => set("enabled", value)}
        />
        <Toggle
          checked={draft.requireVerifiedEmail}
          disabled={!editable}
          hint={translate("fields.verifiedHint")}
          id={verifiedId}
          label={translate("fields.requireVerifiedEmail")}
          onChange={(value) => set("requireVerifiedEmail", value)}
        />
        <Toggle
          checked={draft.allowAutoProvision}
          disabled={!editable}
          hint={translate("fields.provisionHint")}
          id={provisionId}
          label={translate("fields.allowAutoProvision")}
          onChange={(value) => set("allowAutoProvision", value)}
        />
      </div>

      {error && (
        <p className="text-danger-foreground text-sm" role="alert">
          {error.message}
        </p>
      )}

      {editable && (
        <div className="flex flex-wrap items-center gap-2">
          <Button disabled={!complete || pending} onClick={save} size="sm" type="button">
            {translate("save")}
          </Button>
          {onCancel && (
            <Button onClick={onCancel} size="sm" type="button" variant="ghost">
              {translate("cancel")}
            </Button>
          )}
          {known && (
            <Button
              disabled={pending}
              onClick={() => setConfirming(true)}
              size="sm"
              type="button"
              variant="destructive"
            >
              {translate("remove")}
            </Button>
          )}
        </div>
      )}

      <ConfirmDialog
        cancelLabel={translate("cancel")}
        confirmLabel={translate("remove")}
        confirmationPhrase={draft.slug}
        confirmationPrompt={translate("removePrompt", { slug: draft.slug })}
        description={translate("removeDescription")}
        destructive
        onConfirm={async () => {
          const ok = await run(
            `/v1/panel/settings/customer-oidc/${encodeURIComponent(draft.slug)}`,
            { method: "DELETE" },
          );
          setConfirming(false);
          if (ok) {
            onSaved();
          }
        }}
        onOpenChange={setConfirming}
        open={confirming}
        pending={pending}
        title={translate("removeTitle", { name: draft.displayName || draft.slug })}
      />
    </div>
  );
}

function Field({
  children,
  hint,
  id,
  label,
}: {
  children: React.ReactNode;
  hint?: string;
  id: string;
  label: string;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {hint && <p className="text-muted-foreground text-xs">{hint}</p>}
    </div>
  );
}

function Toggle({
  checked,
  disabled,
  hint,
  id,
  label,
  onChange,
}: {
  checked: boolean;
  disabled: boolean;
  hint: string;
  id: string;
  label: string;
  onChange: (value: boolean) => void;
}) {
  return (
    <div className="flex items-start gap-3">
      <Switch checked={checked} disabled={disabled} id={id} onCheckedChange={onChange} />
      <div className="flex flex-col">
        <Label htmlFor={id}>{label}</Label>
        <p className="text-muted-foreground text-xs">{hint}</p>
      </div>
    </div>
  );
}
