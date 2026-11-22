"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Checkbox } from "@omniflow/ui/checkbox";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Switch } from "@omniflow/ui/switch";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";
import { useEnumLabel, usePermissionLabel } from "@/components/admin/enum-label";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";
import type { RbacCatalog } from "../operators/roles";

type Token = {
  id: string;
  name: string;
  prefix: string;
  createdAt: string;
  lastUsedAt?: string;
  revokedAt?: string;
  fullAccess: boolean;
  permissions?: string[] | null;
};
type TokenRequest = { method: string; route: string; status: number; occurredAt: string };
type Webhook = {
  id: string;
  name: string;
  url: string;
  events: string[];
  enabled: boolean;
  failedCount: number;
  lastDeliveredAt?: string;
};
type Delivery = {
  id: string;
  topic: string;
  status: string;
  attempts: number;
  responseStatus?: number;
  createdAt: string;
};

/**
 * Named API tokens for the /v1/admin surface — each with its own request log
 * — and outgoing webhooks that post signed events to another system. A token
 * or signing secret is shown once, when it is created.
 */
export function IntegrationsCard() {
  const translate = useTranslations("admin.integrations");
  const label = useEnumLabel();
  const { can } = useSession();
  const writable = can("system.write");
  const { data: tokens, mutate: refreshTokens } = useSWR<{ items: Token[] }, ApiError>(
    can("system.read") ? "/v1/panel/integrations/tokens" : null,
    fetcher,
  );
  const { data: hooks, mutate: refreshHooks } = useSWR<
    { items: Webhook[]; topics: string[] },
    ApiError
  >(can("system.read") ? "/v1/panel/integrations/webhooks" : null, fetcher);
  const { run, pending } = useOperatorAction();
  const ids = { token: useId(), hookName: useId(), hookUrl: useId() };
  const [tokenName, setTokenName] = useState("");
  const [tokenPermissions, setTokenPermissions] = useState<string[]>([]);
  const [fullAccess, setFullAccess] = useState(false);
  const permissionLabel = usePermissionLabel();
  const { data: catalog } = useSWR<RbacCatalog, ApiError>(
    writable ? "/v1/panel/rbac/catalog" : null,
    fetcher,
  );
  const [hookName, setHookName] = useState("");
  const [hookUrl, setHookUrl] = useState("https://");
  const [hookEvents, setHookEvents] = useState<string[]>([]);
  const [secret, setSecret] = useState<string | null>(null);
  const [openLog, setOpenLog] = useState<string | null>(null);
  const [openDeliveries, setOpenDeliveries] = useState<string | null>(null);
  const { data: log } = useSWR<{ items: TokenRequest[] }, ApiError>(
    openLog ? `/v1/panel/integrations/tokens/${openLog}/requests` : null,
    fetcher,
  );
  const { data: deliveries } = useSWR<{ items: Delivery[] }, ApiError>(
    openDeliveries ? `/v1/panel/integrations/webhooks/${openDeliveries}/deliveries` : null,
    fetcher,
  );

  if (!can("system.read")) {
    return null;
  }

  async function create<T>(path: string, body: unknown): Promise<T | null> {
    try {
      return await apiFetch<T>(path, { method: "POST", body: JSON.stringify(body) });
    } catch {
      toast.error(translate("failed"));
      return null;
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-5">
        {secret ? (
          <div className="flex flex-col gap-1 rounded-md border border-warning p-3">
            <span className="text-sm">{translate("showOnce")}</span>
            <code className="break-all font-mono text-xs">{secret}</code>
            <Button
              className="self-start"
              onClick={() => setSecret(null)}
              size="sm"
              variant="ghost"
            >
              {translate("dismiss")}
            </Button>
          </div>
        ) : null}

        <section className="flex flex-col gap-2">
          <div className="font-medium text-sm">{translate("tokens")}</div>
          {(tokens?.items ?? []).map((token) => (
            <div
              className="flex flex-col gap-1 rounded-md border bg-well p-2 text-sm"
              key={token.id}
            >
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-medium">{token.name}</span>
                <code className="text-muted-foreground text-xs">{token.prefix}…</code>
                {token.revokedAt ? <Badge variant="danger">{translate("revoked")}</Badge> : null}
                {token.fullAccess ? (
                  <Badge variant="warning">{translate("fullAccess")}</Badge>
                ) : (
                  (token.permissions ?? []).map((permission) => (
                    <Badge key={permission} variant="outline">
                      {permissionLabel(permission)}
                    </Badge>
                  ))
                )}
                {token.lastUsedAt ? (
                  <span className="text-muted-foreground text-xs">
                    {translate("lastUsed")} <Timestamp value={token.lastUsedAt} />
                  </span>
                ) : null}
                <Button
                  onClick={() => setOpenLog(openLog === token.id ? null : token.id)}
                  size="sm"
                  variant="ghost"
                >
                  {translate("log")}
                </Button>
                {writable && !token.revokedAt ? (
                  <Button
                    disabled={pending}
                    onClick={async () => {
                      if (
                        await run(`/v1/panel/integrations/tokens/${token.id}/revoke`, {
                          method: "POST",
                        })
                      ) {
                        await refreshTokens();
                      }
                    }}
                    size="sm"
                    variant="ghost"
                  >
                    {translate("revoke")}
                  </Button>
                ) : null}
              </div>
              {openLog === token.id
                ? (log?.items ?? []).map((entry) => (
                    <span className="font-mono text-xs" key={`${entry.occurredAt}-${entry.route}`}>
                      <Timestamp value={entry.occurredAt} /> {entry.method} {entry.route} →{" "}
                      {entry.status}
                    </span>
                  ))
                : null}
            </div>
          ))}
          {writable ? (
            <div className="flex flex-col gap-3 rounded-md border bg-well p-3">
              <div className="flex flex-col gap-1">
                <Label htmlFor={ids.token}>{translate("tokenName")}</Label>
                <Input
                  id={ids.token}
                  onChange={(event) => setTokenName(event.target.value)}
                  value={tokenName}
                />
              </div>
              <fieldset className="flex flex-col gap-2">
                <legend className="mb-1 font-medium text-sm">
                  {translate("tokenPermissions")}
                </legend>
                <p className="text-muted-foreground text-xs">{translate("tokenPermissionsHint")}</p>
                <div className="grid gap-2 sm:grid-cols-3">
                  {(catalog?.permissions ?? []).map((permission) => {
                    const checkboxId = `${ids.token}-${permission}`;
                    return (
                      <div className="flex items-center gap-2" key={permission}>
                        <Checkbox
                          checked={fullAccess || tokenPermissions.includes(permission)}
                          disabled={fullAccess}
                          id={checkboxId}
                          onCheckedChange={(checked) =>
                            setTokenPermissions(
                              checked
                                ? [...tokenPermissions, permission]
                                : tokenPermissions.filter((item) => item !== permission),
                            )
                          }
                        />
                        <Label htmlFor={checkboxId}>{permissionLabel(permission)}</Label>
                      </div>
                    );
                  })}
                </div>
                <div className="flex items-center gap-2">
                  <Switch
                    checked={fullAccess}
                    id={`${ids.token}-full`}
                    onCheckedChange={setFullAccess}
                  />
                  <Label htmlFor={`${ids.token}-full`}>{translate("fullAccessSwitch")}</Label>
                </div>
              </fieldset>
              <Button
                className="self-start"
                disabled={!tokenName.trim() || (!fullAccess && tokenPermissions.length === 0)}
                onClick={async () => {
                  const created = await create<{ token: string }>("/v1/panel/integrations/tokens", {
                    name: tokenName,
                    permissions: fullAccess ? [] : tokenPermissions,
                    fullAccess,
                  });
                  if (created) {
                    setSecret(created.token);
                    setTokenName("");
                    setTokenPermissions([]);
                    setFullAccess(false);
                    await refreshTokens();
                  }
                }}
                size="sm"
              >
                {translate("createToken")}
              </Button>
            </div>
          ) : null}
        </section>

        <section className="flex flex-col gap-2">
          <div className="font-medium text-sm">{translate("webhooks")}</div>
          {(hooks?.items ?? []).map((hook) => (
            <div
              className="flex flex-col gap-1 rounded-md border bg-well p-2 text-sm"
              key={hook.id}
            >
              <div className="flex flex-wrap items-center gap-2">
                <Switch
                  aria-label={translate("enabled")}
                  checked={hook.enabled}
                  disabled={!writable}
                  onCheckedChange={async (enabled) => {
                    if (
                      await run(`/v1/panel/integrations/webhooks/${hook.id}/enabled`, {
                        method: "PUT",
                        body: { enabled },
                      })
                    ) {
                      await refreshHooks();
                    }
                  }}
                />
                <span className="font-medium">{hook.name}</span>
                <code className="break-all text-muted-foreground text-xs">{hook.url}</code>
                {hook.events.map((event) => (
                  <Badge key={event} variant="outline">
                    {event}
                  </Badge>
                ))}
                {hook.failedCount > 0 ? (
                  <Badge variant="danger">
                    {translate("failedCount", { count: hook.failedCount })}
                  </Badge>
                ) : null}
                <Button
                  onClick={() => setOpenDeliveries(openDeliveries === hook.id ? null : hook.id)}
                  size="sm"
                  variant="ghost"
                >
                  {translate("deliveries")}
                </Button>
                {writable ? (
                  <Button
                    disabled={pending}
                    onClick={async () => {
                      if (
                        await run(`/v1/panel/integrations/webhooks/${hook.id}`, {
                          method: "DELETE",
                        })
                      ) {
                        await refreshHooks();
                      }
                    }}
                    size="sm"
                    variant="ghost"
                  >
                    {translate("delete")}
                  </Button>
                ) : null}
              </div>
              {openDeliveries === hook.id
                ? (deliveries?.items ?? []).map((delivery) => (
                    <span className="font-mono text-xs" key={delivery.id}>
                      <Timestamp value={delivery.createdAt} /> {delivery.topic} ·{" "}
                      {label("deliveryStatus", delivery.status)} · {delivery.attempts}×{" "}
                      {delivery.responseStatus ?? ""}
                    </span>
                  ))
                : null}
            </div>
          ))}
          {writable ? (
            <div className="flex flex-col gap-2">
              <div className="grid gap-2 sm:grid-cols-2">
                <div className="flex flex-col gap-1">
                  <Label htmlFor={ids.hookName}>{translate("hookName")}</Label>
                  <Input
                    id={ids.hookName}
                    onChange={(event) => setHookName(event.target.value)}
                    value={hookName}
                  />
                </div>
                <div className="flex flex-col gap-1">
                  <Label htmlFor={ids.hookUrl}>{translate("hookUrl")}</Label>
                  <Input
                    id={ids.hookUrl}
                    onChange={(event) => setHookUrl(event.target.value)}
                    type="url"
                    value={hookUrl}
                  />
                </div>
              </div>
              <div className="flex flex-wrap gap-4">
                {(hooks?.topics ?? []).map((topic) => (
                  <div className="flex items-center gap-2" key={topic}>
                    <Switch
                      checked={hookEvents.includes(topic)}
                      id={`${ids.hookName}-${topic}`}
                      onCheckedChange={(on) =>
                        setHookEvents(
                          on ? [...hookEvents, topic] : hookEvents.filter((item) => item !== topic),
                        )
                      }
                    />
                    <Label htmlFor={`${ids.hookName}-${topic}`}>{topic}</Label>
                  </div>
                ))}
              </div>
              <Button
                className="self-start"
                disabled={!hookName.trim() || hookEvents.length === 0}
                onClick={async () => {
                  const created = await create<{ secret: string }>(
                    "/v1/panel/integrations/webhooks",
                    {
                      name: hookName,
                      url: hookUrl,
                      events: hookEvents,
                    },
                  );
                  if (created) {
                    setSecret(created.secret);
                    setHookName("");
                    setHookUrl("https://");
                    setHookEvents([]);
                    await refreshHooks();
                  }
                }}
                size="sm"
              >
                {translate("createWebhook")}
              </Button>
            </div>
          ) : null}
        </section>
      </CardContent>
    </Card>
  );
}
