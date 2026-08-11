"use client";

import { Alert, AlertDescription, AlertTitle } from "@omniflow/ui/alert";
import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { PageHeader } from "@/components/admin/resource-table";
import { type ApiError, fetcher } from "@/lib/api";
import { formatBytes, formatDuration, type Listing, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

import type {
  AiFeature,
  AiFeatureListing,
  AiProvider,
  AiUsageLimit,
  AiUsageReport,
  McpEventPage,
  McpServer,
  McpTool,
} from "./types";

/**
 * AI providers, per-feature enablement, usage limits, spend, and the MCP
 * registry.
 *
 * The screen is arranged around one idea: an owner should be able to see what
 * leaves the installation before they turn anything on. So the provider's
 * retention and training answers sit above the switches that would send data to
 * it, the warnings for a feature render next to that feature's switch rather
 * than in a separate panel, and a credential is never rendered — only whether
 * one exists.
 */
export function AiSettings() {
  const translate = useTranslations("admin.aiSettings");
  const { can } = useSession();
  const editable = can("settings.write");

  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />
      <ProviderCard editable={editable} />
      <FeatureCard editable={editable} />
      <LimitCard editable={editable} />
      <UsageCard />
      <McpCard editable={editable} />
      <McpAuditCard />
    </div>
  );
}

function ProviderCard({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.aiSettings");
  const { data, isLoading, mutate } = useSWR<Listing<AiProvider>, ApiError>(
    "/v1/panel/settings/ai/providers",
    fetcher,
  );
  const providers = data?.items ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("providers.title")}</CardTitle>
        <CardDescription>{translate("providers.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {isLoading ? <Skeleton className="h-32 w-full" /> : null}
        {!isLoading && providers.length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("providers.empty")}</p>
        ) : null}
        {providers.map((provider) => (
          <ProviderRow
            editable={editable}
            key={provider.slug}
            onSaved={() => mutate()}
            provider={provider}
          />
        ))}
        {editable ? <ProviderEditor onSaved={() => mutate()} /> : null}
      </CardContent>
    </Card>
  );
}

function ProviderRow({
  provider,
  editable,
  onSaved,
}: {
  provider: AiProvider;
  editable: boolean;
  onSaved: () => void;
}) {
  const translate = useTranslations("admin.aiSettings");
  const { run, pending } = useOperatorAction();
  const [key, setKey] = useState("");
  const keyId = useId();

  async function save(patch: Partial<AiProvider>) {
    const saved = await run("/v1/panel/settings/ai/providers", {
      method: "PUT",
      body: { ...provider, ...patch, apiKey: key || undefined },
      reason: translate("providers.reason"),
    });
    if (saved) {
      setKey("");
      onSaved();
    }
  }

  return (
    <div className="flex flex-col gap-3 rounded-md border p-4">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">{provider.displayName}</span>
        <Badge variant="outline">{provider.kind}</Badge>
        {/* What the provider does with the data sits above the switch that
            would send data to it, not in a footnote below. */}
        {provider.zeroRetention ? (
          <Badge variant="neutral">{translate("providers.zeroRetention")}</Badge>
        ) : null}
        {provider.trainsOnData ? (
          <Badge variant="danger">{translate("providers.trainsOnData")}</Badge>
        ) : null}
        {provider.dataRegion ? <Badge variant="outline">{provider.dataRegion}</Badge> : null}
        <Badge className="ml-auto" variant={provider.keyConfigured ? "neutral" : "outline"}>
          {provider.keyConfigured
            ? translate("providers.keyConfigured")
            : translate("providers.keyMissing")}
        </Badge>
      </div>

      {provider.baseUrl ? (
        <p className="font-mono text-muted-foreground text-xs">{provider.baseUrl}</p>
      ) : null}
      {provider.retentionNote ? (
        <p className="text-muted-foreground text-sm">{provider.retentionNote}</p>
      ) : null}

      <div className="flex flex-wrap items-center gap-3">
        <Switch
          checked={provider.enabled}
          disabled={!editable || pending}
          id={`${provider.slug}-enabled`}
          onCheckedChange={(enabled) => save({ enabled })}
        />
        <Label htmlFor={`${provider.slug}-enabled`}>{translate("providers.enabled")}</Label>
        <span className="text-muted-foreground text-xs">
          {provider.lastCheckedAt
            ? provider.lastCheckOk
              ? translate("providers.checkOk")
              : translate("providers.checkFailed", { detail: provider.lastCheckError ?? "" })
            : translate("providers.neverChecked")}
        </span>
      </div>

      {editable ? (
        <div className="flex flex-wrap items-end gap-2">
          <div className="flex flex-1 flex-col gap-1">
            <Label htmlFor={keyId}>{translate("providers.apiKey")}</Label>
            {/* Write-only: the field is always empty on load because the API
                never returns the stored value, and leaving it empty keeps the
                existing credential. */}
            <Input
              autoComplete="off"
              id={keyId}
              onChange={(event) => setKey(event.target.value)}
              placeholder={translate("providers.apiKeyPlaceholder")}
              type="password"
              value={key}
            />
          </div>
          <Button disabled={pending || key.length === 0} onClick={() => save({})} type="button">
            {translate("providers.rotate")}
          </Button>
        </div>
      ) : null}
    </div>
  );
}

function ProviderEditor({ onSaved }: { onSaved: () => void }) {
  const translate = useTranslations("admin.aiSettings");
  const { run, pending, error } = useOperatorAction();
  const [form, setForm] = useState({
    apiKey: "",
    baseUrl: "",
    displayName: "",
    kind: "openai_compatible" as AiProvider["kind"],
    slug: "",
    trainsOnData: false,
    zeroRetention: false,
  });
  const slugId = useId();
  const nameId = useId();
  const baseId = useId();
  const zeroRetentionId = useId();
  const trainsId = useId();

  async function create() {
    const saved = await run("/v1/panel/settings/ai/providers", {
      method: "PUT",
      body: { ...form, enabled: false },
      reason: translate("providers.reason"),
    });
    if (saved) {
      setForm({ ...form, apiKey: "", baseUrl: "", displayName: "", slug: "" });
      onSaved();
    }
  }

  return (
    <div className="flex flex-col gap-3 rounded-md border border-dashed p-4">
      <p className="font-medium text-sm">{translate("providers.addTitle")}</p>
      <div className="grid gap-3 sm:grid-cols-3">
        <div className="flex flex-col gap-1">
          <Label htmlFor={slugId}>{translate("providers.slug")}</Label>
          <Input
            id={slugId}
            onChange={(event) => setForm({ ...form, slug: event.target.value })}
            value={form.slug}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={nameId}>{translate("providers.displayName")}</Label>
          <Input
            id={nameId}
            onChange={(event) => setForm({ ...form, displayName: event.target.value })}
            value={form.displayName}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={baseId}>{translate("providers.baseUrl")}</Label>
          <Input
            id={baseId}
            onChange={(event) => setForm({ ...form, baseUrl: event.target.value })}
            placeholder="https://…"
            value={form.baseUrl}
          />
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-4">
        {(["openai_compatible", "anthropic", "gemini"] as const).map((kind) => (
          <label className="flex items-center gap-2 text-sm" key={kind}>
            <input
              checked={form.kind === kind}
              name="ai-provider-kind"
              onChange={() => setForm({ ...form, kind })}
              type="radio"
            />
            {kind}
          </label>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-4">
        <div className="flex items-center gap-2 text-sm">
          <Switch
            checked={form.zeroRetention}
            id={zeroRetentionId}
            onCheckedChange={(zeroRetention) => setForm({ ...form, zeroRetention })}
          />
          <Label htmlFor={zeroRetentionId}>{translate("providers.zeroRetention")}</Label>
        </div>
        <div className="flex items-center gap-2 text-sm">
          <Switch
            checked={form.trainsOnData}
            id={trainsId}
            onCheckedChange={(trainsOnData) => setForm({ ...form, trainsOnData })}
          />
          <Label htmlFor={trainsId}>{translate("providers.trainsOnData")}</Label>
        </div>
      </div>
      {error ? <p className="text-destructive text-sm">{translate("saveFailed")}</p> : null}
      <Button
        className="self-start"
        disabled={pending || form.slug.length === 0}
        onClick={create}
        type="button"
      >
        {translate("providers.add")}
      </Button>
    </div>
  );
}

function FeatureCard({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.aiSettings");
  const { data, isLoading, mutate } = useSWR<AiFeatureListing, ApiError>(
    "/v1/panel/settings/ai/features",
    fetcher,
  );
  const features = data?.items ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("features.title")}</CardTitle>
        <CardDescription>{translate("features.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {isLoading ? <Skeleton className="h-40 w-full" /> : null}
        {features.map((feature) => (
          <FeatureRow
            editable={editable}
            feature={feature}
            key={feature.feature}
            onSaved={() => mutate()}
          />
        ))}
      </CardContent>
    </Card>
  );
}

function FeatureRow({
  feature,
  editable,
  onSaved,
}: {
  feature: AiFeature;
  editable: boolean;
  onSaved: () => void;
}) {
  const translate = useTranslations("admin.aiSettings");
  const { run, pending } = useOperatorAction();
  const [model, setModel] = useState(feature.model ?? "");
  const [provider, setProvider] = useState(feature.provider ?? "");
  const [retentionDays, setRetentionDays] = useState(String(feature.retentionDays));
  const modelId = useId();
  const providerId = useId();
  const retentionId = useId();

  const blocked = feature.warnings.some((warning) => warning.blocking);

  async function save(patch: Partial<AiFeature>) {
    const saved = await run(`/v1/panel/settings/ai/features/${feature.feature}`, {
      method: "PUT",
      body: {
        ...feature,
        model,
        provider,
        retentionDays: Number(retentionDays) || 0,
        ...patch,
      },
      reason: translate("features.reason"),
    });
    if (saved) {
      onSaved();
    }
  }

  return (
    <div className="flex flex-col gap-3 rounded-md border p-4">
      <div className="flex flex-wrap items-center gap-3">
        <Switch
          // A feature the server would refuse cannot be switched on here
          // either, so the control and the rule agree.
          checked={feature.enabled}
          disabled={!editable || pending || (blocked && !feature.enabled)}
          id={`${feature.feature}-enabled`}
          onCheckedChange={(enabled) => save({ enabled })}
        />
        <Label htmlFor={`${feature.feature}-enabled`}>
          {translate(`features.names.${feature.feature}`)}
        </Label>
        {feature.enabled && blocked ? (
          <Badge variant="danger">{translate("features.misconfigured")}</Badge>
        ) : null}
      </div>

      {/* The warnings sit next to the switch they are about, because a warning
          in a separate panel is one an operator reads after deciding. */}
      {feature.warnings.length > 0 ? (
        <div className="flex flex-col gap-2">
          {feature.warnings.map((warning) => (
            <Alert key={warning.code} variant={warning.blocking ? "danger" : "default"}>
              <AlertTitle>{translate(`features.warnings.${warning.code}`)}</AlertTitle>
              <AlertDescription>{warning.text}</AlertDescription>
            </Alert>
          ))}
        </div>
      ) : null}

      {editable ? (
        <div className="grid gap-3 sm:grid-cols-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor={providerId}>{translate("features.provider")}</Label>
            <Input
              id={providerId}
              onBlur={() => save({})}
              onChange={(event) => setProvider(event.target.value)}
              value={provider}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={modelId}>{translate("features.model")}</Label>
            <Input
              id={modelId}
              onBlur={() => save({})}
              onChange={(event) => setModel(event.target.value)}
              value={model}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={retentionId}>{translate("features.retentionDays")}</Label>
            <Input
              id={retentionId}
              inputMode="numeric"
              onBlur={() => save({})}
              onChange={(event) => setRetentionDays(event.target.value)}
              value={retentionDays}
            />
          </div>
        </div>
      ) : null}

      <div className="flex flex-wrap items-center gap-4 text-sm">
        <div className="flex items-center gap-2">
          <Switch
            checked={feature.retainPrompts}
            disabled={!editable || pending}
            id={`${feature.feature}-prompts`}
            onCheckedChange={(retainPrompts) => save({ retainPrompts })}
          />
          <Label htmlFor={`${feature.feature}-prompts`}>
            {translate("features.retainPrompts")}
          </Label>
        </div>
        <div className="flex items-center gap-2">
          <Switch
            checked={feature.retainOutputs}
            disabled={!editable || pending}
            id={`${feature.feature}-outputs`}
            onCheckedChange={(retainOutputs) => save({ retainOutputs })}
          />
          <Label htmlFor={`${feature.feature}-outputs`}>
            {translate("features.retainOutputs")}
          </Label>
        </div>
      </div>
    </div>
  );
}

function LimitCard({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.aiSettings");
  const { data, isLoading, mutate } = useSWR<Listing<AiUsageLimit>, ApiError>(
    "/v1/panel/settings/ai/limits",
    fetcher,
  );
  const { run, pending } = useOperatorAction();
  const limits = data?.items ?? [];

  const [form, setForm] = useState({
    feature: "",
    maxRequests: "",
    ref: "",
    scope: "installation" as AiUsageLimit["scope"],
    windowHours: "24",
  });
  const refId = useId();
  const requestsId = useId();

  async function create() {
    const saved = await run("/v1/panel/settings/ai/limits", {
      method: "PUT",
      body: {
        feature: form.feature || undefined,
        maxRequests: Number(form.maxRequests) || undefined,
        ref: form.ref || undefined,
        scope: form.scope,
        windowSeconds: (Number(form.windowHours) || 24) * 3600,
      },
      reason: translate("limits.reason"),
    });
    if (saved) {
      setForm({ ...form, maxRequests: "", ref: "" });
      mutate();
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("limits.title")}</CardTitle>
        <CardDescription>{translate("limits.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {isLoading ? <Skeleton className="h-24 w-full" /> : null}
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{translate("limits.scope")}</TableHead>
              <TableHead>{translate("limits.window")}</TableHead>
              <TableHead>{translate("limits.requests")}</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {limits.map((limit) => (
              <TableRow key={limit.id ?? `${limit.scope}-${limit.ref}-${limit.feature}`}>
                <TableCell>
                  {limit.scope}
                  {limit.ref ? ` · ${limit.ref}` : ""}
                  {limit.feature ? ` · ${limit.feature}` : ""}
                </TableCell>
                <TableCell>{formatDuration(limit.windowSeconds)}</TableCell>
                <TableCell>{limit.maxRequests ?? "—"}</TableCell>
                <TableCell className="text-right">
                  {editable && limit.id ? (
                    <Button
                      disabled={pending}
                      onClick={async () => {
                        const removed = await run(`/v1/panel/settings/ai/limits/${limit.id}`, {
                          method: "DELETE",
                          reason: translate("limits.reason"),
                        });
                        if (removed) {
                          mutate();
                        }
                      }}
                      size="sm"
                      type="button"
                      variant="ghost"
                    >
                      {translate("remove")}
                    </Button>
                  ) : null}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>

        {editable ? (
          <div className="flex flex-wrap items-end gap-2 rounded-md border border-dashed p-4">
            <div className="flex flex-col gap-1">
              <Label htmlFor={refId}>{translate("limits.ref")}</Label>
              <Input
                id={refId}
                onChange={(event) => setForm({ ...form, ref: event.target.value })}
                placeholder={translate("limits.refPlaceholder")}
                value={form.ref}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={requestsId}>{translate("limits.requests")}</Label>
              <Input
                id={requestsId}
                inputMode="numeric"
                onChange={(event) => setForm({ ...form, maxRequests: event.target.value })}
                value={form.maxRequests}
              />
            </div>
            <div className="flex flex-wrap items-center gap-3">
              {(["installation", "role", "operator", "feature"] as const).map((scope) => (
                <label className="flex items-center gap-2 text-sm" key={scope}>
                  <input
                    checked={form.scope === scope}
                    name="ai-limit-scope"
                    onChange={() => setForm({ ...form, scope })}
                    type="radio"
                  />
                  {scope}
                </label>
              ))}
            </div>
            <Button disabled={pending} onClick={create} type="button">
              {translate("limits.add")}
            </Button>
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}

function UsageCard() {
  const translate = useTranslations("admin.aiSettings");
  const { data, isLoading } = useSWR<AiUsageReport, ApiError>(
    "/v1/panel/settings/ai/usage",
    fetcher,
  );
  const rows = data?.items ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("usage.title")}</CardTitle>
        {/* The window is stated beside the numbers, because a figure whose
            period the reader has to guess is one they will misread. */}
        <CardDescription>
          {data
            ? translate("usage.window", {
                since: new Date(data.since).toLocaleDateString(),
                until: new Date(data.until).toLocaleDateString(),
              })
            : translate("usage.description")}
        </CardDescription>
      </CardHeader>
      <CardContent>
        {isLoading ? <Skeleton className="h-24 w-full" /> : null}
        {!isLoading && rows.length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("usage.empty")}</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("usage.feature")}</TableHead>
                <TableHead>{translate("usage.model")}</TableHead>
                <TableHead className="text-right">{translate("usage.requests")}</TableHead>
                <TableHead className="text-right">{translate("usage.tokens")}</TableHead>
                <TableHead className="text-right">{translate("usage.latency")}</TableHead>
                <TableHead className="text-right">{translate("usage.failures")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((row) => (
                <TableRow key={`${row.feature}-${row.provider}-${row.model}`}>
                  <TableCell>{row.feature}</TableCell>
                  <TableCell className="font-mono text-xs">
                    {row.provider} · {row.model}
                  </TableCell>
                  <TableCell className="text-right">{row.requests}</TableCell>
                  <TableCell className="text-right">{row.inputTokens + row.outputTokens}</TableCell>
                  <TableCell className="text-right">
                    {row.meanLatencyMs}ms / {row.p95LatencyMs}ms
                  </TableCell>
                  <TableCell className="text-right">{row.failures}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

function McpCard({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.aiSettings");
  const { data, isLoading, mutate } = useSWR<Listing<McpServer>, ApiError>(
    "/v1/panel/settings/mcp/servers",
    fetcher,
  );
  const servers = data?.items ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("mcp.title")}</CardTitle>
        <CardDescription>{translate("mcp.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {isLoading ? <Skeleton className="h-32 w-full" /> : null}
        {!isLoading && servers.length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("mcp.empty")}</p>
        ) : null}
        {servers.map((server) => (
          <McpServerRow
            editable={editable}
            key={server.slug}
            onSaved={() => mutate()}
            server={server}
          />
        ))}
      </CardContent>
    </Card>
  );
}

function McpServerRow({
  server,
  editable,
  onSaved,
}: {
  server: McpServer;
  editable: boolean;
  onSaved: () => void;
}) {
  const translate = useTranslations("admin.aiSettings");
  const { run, pending } = useOperatorAction();
  const { data } = useSWR<Listing<McpTool>, ApiError>(
    `/v1/panel/settings/mcp/servers/${server.slug}/tools`,
    fetcher,
  );
  const tools = data?.items ?? [];

  return (
    <div className="flex flex-col gap-3 rounded-md border p-4">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">{server.displayName}</span>
        <Badge variant="outline">{server.protocolVersion ?? translate("mcp.notDiscovered")}</Badge>
        {server.allowPrivateNetwork ? (
          <Badge variant="danger">{translate("mcp.privateNetwork")}</Badge>
        ) : null}
        <Badge className="ml-auto" variant={server.lastCheckOk ? "neutral" : "danger"}>
          {server.lastCheckedAt
            ? server.lastCheckOk
              ? translate("mcp.reachable")
              : translate("mcp.unreachable", { failures: server.consecutiveFailures })
            : translate("mcp.neverContacted")}
        </Badge>
      </div>
      <p className="break-all font-mono text-muted-foreground text-xs">{server.endpoint}</p>
      <p className="text-muted-foreground text-xs">
        {translate("mcp.limits", {
          bytes: formatBytes(server.maxResponseBytes),
          calls: server.maxCallsPerRequest,
          depth: server.maxDepth,
          timeout: formatDuration(server.timeoutMs / 1000),
        })}
      </p>

      <div className="flex items-center gap-3">
        <Switch
          checked={server.enabled}
          disabled={!editable || pending}
          id={`${server.slug}-enabled`}
          onCheckedChange={async (enabled) => {
            const saved = await run("/v1/panel/settings/mcp/servers", {
              method: "PUT",
              body: { ...server, enabled },
              reason: translate("mcp.reason"),
            });
            if (saved) {
              onSaved();
            }
          }}
        />
        <Label htmlFor={`${server.slug}-enabled`}>{translate("mcp.enabled")}</Label>
      </div>

      {/* Discovery is not authorisation: everything the server advertises is
          listed, and only what an owner allowlisted is callable. */}
      {tools.length > 0 ? (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{translate("mcp.tool")}</TableHead>
              <TableHead>{translate("mcp.permission")}</TableHead>
              <TableHead>{translate("mcp.writes")}</TableHead>
              <TableHead>{translate("mcp.allowed")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {tools.map((tool) => (
              <TableRow key={tool.tool}>
                <TableCell>
                  <span className="font-mono text-xs">{tool.tool}</span>
                  {!tool.schemaUsable ? (
                    <p className="text-destructive text-xs">
                      {tool.schemaProblem ?? translate("mcp.schemaUnusable")}
                    </p>
                  ) : null}
                </TableCell>
                <TableCell className="font-mono text-xs">{tool.permission}</TableCell>
                <TableCell>
                  {tool.writes ? (
                    <Badge variant="danger">{translate("mcp.writesYes")}</Badge>
                  ) : (
                    <Badge variant="outline">{translate("mcp.writesNo")}</Badge>
                  )}
                </TableCell>
                <TableCell>
                  <Switch
                    // A tool whose schema this build cannot enforce cannot be
                    // enabled, because enabling it would mean forwarding
                    // unvalidated arguments.
                    checked={tool.enabled}
                    disabled={!editable || pending || !tool.schemaUsable}
                    onCheckedChange={async (enabled) => {
                      await run(
                        `/v1/panel/settings/mcp/servers/${server.slug}/tools/${tool.tool}`,
                        {
                          method: "PUT",
                          body: { enabled, permission: tool.permission, writes: tool.writes },
                          reason: translate("mcp.reason"),
                        },
                      );
                      onSaved();
                    }}
                  />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      ) : null}
    </div>
  );
}

function McpAuditCard() {
  const translate = useTranslations("admin.aiSettings");
  const { data, isLoading } = useSWR<McpEventPage, ApiError>(
    "/v1/panel/settings/mcp/events?pageSize=25",
    fetcher,
  );
  const events = data?.items ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("mcpAudit.title")}</CardTitle>
        {/* Refusals are listed alongside successes, because a trail that only
            records what happened cannot answer "did anyone try?". */}
        <CardDescription>{translate("mcpAudit.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        {isLoading ? <Skeleton className="h-24 w-full" /> : null}
        {!isLoading && events.length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("mcpAudit.empty")}</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("mcpAudit.when")}</TableHead>
                <TableHead>{translate("mcpAudit.what")}</TableHead>
                <TableHead>{translate("mcpAudit.outcome")}</TableHead>
                <TableHead>{translate("mcpAudit.detail")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {events.map((event) => (
                <TableRow key={event.id}>
                  <TableCell className="whitespace-nowrap text-xs">
                    {new Date(event.occurredAt).toLocaleString()}
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {event.server ?? "—"}
                    {event.tool ? ` / ${event.tool}` : ""}
                  </TableCell>
                  <TableCell>
                    <Badge
                      variant={
                        event.outcome === "allowed"
                          ? "neutral"
                          : event.outcome === "replayed"
                            ? "outline"
                            : "danger"
                      }
                    >
                      {translate(`mcpAudit.outcomes.${event.outcome}`)}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-xs">
                    {event.detail}
                    {(event.findings ?? []).length > 0 ? (
                      <span className="ml-2 text-destructive">
                        {(event.findings ?? []).join(", ")}
                      </span>
                    ) : null}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}
