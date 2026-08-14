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

import { type ApiError, fetcher } from "@/lib/api";
import { formatBytes, type Listing, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

import {
  type SectionField,
  type SectionSchema,
  type SettingsGroupKey,
  sectionsInGroup,
} from "./sections";

type SettingSection = {
  section: string;
  document: Record<string, unknown>;
  secretConfigured: boolean;
  version: number;
  updatedAt?: string;
};

type TelemetryPreview = {
  enabled: boolean;
  installationId: string;
  endpoint?: string;
  payload: unknown;
  fields: string[] | null;
  lastSentAt?: string;
  eventCount: number;
};

type BackupEntry = {
  id: string;
  kind: string;
  status: string;
  sizeBytes: number;
  createdAt: string;
  verifiedAt?: string;
  requestedBy?: string;
  detail?: string;
};

/**
 * One group of the installation's own configuration.
 *
 * It renders the sections belonging to a single group rather than all ten,
 * because all ten on one page — under commerce and above every sign-in provider
 * — is what made the settings screen something an operator scrolled through
 * looking for the thing they came for.
 *
 * Every section saves with the version it was rendered from, so two operators
 * editing the same screen produce a conflict rather than one silently
 * overwriting the other. Secrets are write-only end to end: the field is always
 * empty because the API never returns a value, and leaving it empty keeps the
 * stored one — which is what makes rotating a token a deliberate act rather
 * than a side effect of editing the field next to it.
 */
export function InstallationSettings({ group }: { group: SettingsGroupKey }) {
  const translate = useTranslations("admin.installationSettings");
  const { can } = useSession();
  const editable = can("settings.write");

  const { data, isLoading, mutate } = useSWR<Listing<SettingSection>, ApiError>(
    "/v1/panel/settings",
    fetcher,
  );
  const sections = data?.items ?? [];
  const schemas = sectionsInGroup(group);

  if (isLoading) {
    return <Skeleton className="h-96 w-full" />;
  }

  return (
    <div className="flex flex-col gap-5">
      {schemas.map((schema) => {
        const stored = sections.find((candidate) => candidate.section === schema.section);
        if (!stored) {
          return null;
        }
        return (
          <SectionCard
            editable={editable}
            key={schema.section}
            onSaved={() => mutate()}
            schema={schema}
            stored={stored}
          />
        );
      })}
      {/* The telemetry preview and the backup history belong beside the
          settings they report on, not at the foot of a page that also held
          payment providers. */}
      {group === "operations" ? (
        <>
          <TelemetryCard />
          <BackupCard />
        </>
      ) : null}
      {schemas.some((schema) => schema.fields.some((field) => field.kind === "secret")) ? (
        <p className="text-muted-foreground text-xs">{translate("secretsNote")}</p>
      ) : null}
    </div>
  );
}

function SectionCard({
  schema,
  stored,
  editable,
  onSaved,
}: {
  schema: SectionSchema;
  stored: SettingSection;
  editable: boolean;
  onSaved: () => void;
}) {
  const translate = useTranslations("admin.installationSettings");
  const { run, pending, error } = useOperatorAction();
  const [document, setDocument] = useState<Record<string, unknown>>(stored.document ?? {});
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [conflict, setConflict] = useState(false);
  const fieldId = useId();

  async function save() {
    setConflict(false);
    const saved = await run(`/v1/panel/settings/${schema.section}`, {
      method: "PUT",
      body: {
        document,
        version: stored.version,
        // An empty secret is omitted rather than sent as "", which would
        // otherwise clear a stored credential by accident.
        secrets: Object.fromEntries(
          Object.entries(secrets).filter(([, value]) => value.trim().length > 0),
        ),
      },
      reason: translate("reason", { section: translate(`sections.${schema.messageKey}.title`) }),
    });
    if (saved) {
      setSecrets({});
      onSaved();
      return;
    }
    // A version mismatch comes back as a conflict. Saying so, and reloading,
    // is the only honest response: the operator's screen is out of date and
    // saving again would discard whatever the other change was.
    if (error?.status === 409) {
      setConflict(true);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate(`sections.${schema.messageKey}.title`)}</CardTitle>
        <CardDescription>{translate(`sections.${schema.messageKey}.description`)}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {conflict ? (
          <Alert variant="warning">
            <AlertTitle>{translate("conflictTitle")}</AlertTitle>
            <AlertDescription>{translate("conflict")}</AlertDescription>
          </Alert>
        ) : null}

        <div className="grid gap-3 sm:grid-cols-2">
          {schema.fields.map((field) => (
            <FieldControl
              document={document}
              editable={editable}
              field={field}
              id={`${fieldId}-${field.name}`}
              key={field.name}
              onDocument={setDocument}
              onSecret={setSecrets}
              secretConfigured={stored.secretConfigured}
              secrets={secrets}
            />
          ))}
        </div>

        <div className="flex flex-wrap items-center gap-3">
          {editable ? (
            <Button disabled={pending} onClick={save} type="button">
              {translate("save")}
            </Button>
          ) : null}
          <span className="text-muted-foreground text-xs">
            {stored.updatedAt
              ? translate("lastSaved", {
                  when: new Date(stored.updatedAt).toLocaleString(),
                  version: stored.version,
                })
              : translate("neverSaved")}
          </span>
        </div>
      </CardContent>
    </Card>
  );
}

function FieldControl({
  field,
  document,
  secrets,
  secretConfigured,
  editable,
  id,
  onDocument,
  onSecret,
}: {
  field: SectionField;
  document: Record<string, unknown>;
  secrets: Record<string, string>;
  secretConfigured: boolean;
  editable: boolean;
  id: string;
  onDocument: (next: Record<string, unknown>) => void;
  onSecret: (next: Record<string, string>) => void;
}) {
  const translate = useTranslations("admin.installationSettings");
  const label = translate(`fields.${field.messageKey}`);

  if (field.kind === "boolean") {
    return (
      <div className="flex items-center gap-3">
        <Switch
          checked={Boolean(document[field.name])}
          disabled={!editable}
          id={id}
          onCheckedChange={(value) => onDocument({ ...document, [field.name]: value })}
        />
        <Label htmlFor={id}>{label}</Label>
      </div>
    );
  }

  if (field.kind === "secret") {
    return (
      <div className="flex flex-col gap-1">
        <div className="flex items-center gap-2">
          <Label htmlFor={id}>{label}</Label>
          {/* "Configured" is the only safe rendering of a stored secret. */}
          <Badge variant={secretConfigured ? "neutral" : "outline"}>
            {secretConfigured ? translate("configured") : translate("notConfigured")}
          </Badge>
        </div>
        <Input
          autoComplete="off"
          disabled={!editable}
          id={id}
          onChange={(event) => onSecret({ ...secrets, [field.name]: event.target.value })}
          placeholder={translate("secretPlaceholder")}
          type="password"
          value={secrets[field.name] ?? ""}
        />
      </div>
    );
  }

  if (field.kind === "textarea") {
    return (
      <div className="flex flex-col gap-1 sm:col-span-2">
        <Label htmlFor={id}>{label}</Label>
        <textarea
          className="min-h-20 rounded-md border border-border bg-transparent p-2 text-sm"
          disabled={!editable}
          id={id}
          onChange={(event) => onDocument({ ...document, [field.name]: event.target.value })}
          value={String(document[field.name] ?? "")}
        />
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{label}</Label>
      <Input
        disabled={!editable}
        id={id}
        inputMode={field.kind === "number" ? "numeric" : undefined}
        onChange={(event) =>
          onDocument({
            ...document,
            [field.name]:
              field.kind === "number" ? Number(event.target.value) || 0 : event.target.value,
          })
        }
        type={field.kind === "url" ? "url" : "text"}
        value={String(document[field.name] ?? "")}
      />
    </div>
  );
}

function TelemetryCard() {
  const translate = useTranslations("admin.installationSettings");
  const { data, isLoading } = useSWR<TelemetryPreview, ApiError>(
    "/v1/panel/settings/telemetry/preview",
    fetcher,
  );

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("telemetryPreview.title")}</CardTitle>
        {/* The exact payload, rendered from the same values the sender uses. A
            preview assembled separately is a promise about a different
            program. */}
        <CardDescription>{translate("telemetryPreview.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {isLoading || !data ? (
          <Skeleton className="h-32 w-full" />
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant={data.enabled ? "neutral" : "outline"}>
                {data.enabled
                  ? translate("telemetryPreview.on")
                  : translate("telemetryPreview.off")}
              </Badge>
              <span className="text-muted-foreground text-xs">
                {data.lastSentAt
                  ? translate("telemetryPreview.lastSent", {
                      when: new Date(data.lastSentAt).toLocaleString(),
                    })
                  : translate("telemetryPreview.neverSent")}
              </span>
            </div>
            <pre className="overflow-x-auto rounded-md border bg-muted/40 p-3 text-xs">
              {JSON.stringify(data.payload, null, 2)}
            </pre>
            <p className="text-muted-foreground text-xs">
              {translate("telemetryPreview.fields", {
                fields: (data.fields ?? []).join(", "),
              })}
            </p>
          </>
        )}
      </CardContent>
    </Card>
  );
}

function BackupCard() {
  const translate = useTranslations("admin.installationSettings");
  const { data, isLoading } = useSWR<{
    backups: BackupEntry[] | null;
    restores: BackupEntry[] | null;
  }>("/v1/panel/settings/backups", fetcher);

  const backups = data?.backups ?? [];
  const restores = data?.restores ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("backups.title")}</CardTitle>
        {/* A backup nobody has ever restored is a backup nobody knows works, so
            the restore history sits beside the backup list. */}
        <CardDescription>{translate("backups.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {isLoading ? <Skeleton className="h-24 w-full" /> : null}
        {backups.length === 0 && !isLoading ? (
          <p className="text-muted-foreground text-sm">{translate("backups.empty")}</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("backups.when")}</TableHead>
                <TableHead>{translate("backups.kind")}</TableHead>
                <TableHead>{translate("backups.status")}</TableHead>
                <TableHead className="text-right">{translate("backups.size")}</TableHead>
                <TableHead>{translate("backups.verified")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {backups.map((backup) => (
                <TableRow key={backup.id}>
                  <TableCell className="whitespace-nowrap text-xs">
                    {new Date(backup.createdAt).toLocaleString()}
                  </TableCell>
                  <TableCell>{backup.kind}</TableCell>
                  <TableCell>
                    <Badge variant={backup.status === "completed" ? "neutral" : "danger"}>
                      {backup.status}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-right">{formatBytes(backup.sizeBytes)}</TableCell>
                  <TableCell className="text-xs">
                    {backup.verifiedAt
                      ? new Date(backup.verifiedAt).toLocaleDateString()
                      : translate("backups.unverified")}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}

        <div>
          <p className="mb-2 font-medium text-sm">{translate("backups.restores")}</p>
          {restores.length === 0 ? (
            <p className="text-muted-foreground text-sm">{translate("backups.noRestores")}</p>
          ) : (
            <ul className="flex flex-col gap-1 text-sm">
              {restores.map((restore) => (
                <li key={restore.id}>
                  {new Date(restore.createdAt).toLocaleString()} · {restore.status}
                  {restore.detail ? ` · ${restore.detail}` : ""}
                </li>
              ))}
            </ul>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
