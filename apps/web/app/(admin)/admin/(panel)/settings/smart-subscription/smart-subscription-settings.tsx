"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@omniflow/ui/command";
import { Input, Textarea } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { cn } from "@omniflow/ui/lib/utils";
import { Popover, PopoverContent, PopoverTrigger } from "@omniflow/ui/popover";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { toast } from "@omniflow/ui/toast";
import { Check, ChevronsUpDown, Minus, TriangleAlert, X } from "lucide-react";
import { useLocale, useTranslations } from "next-intl";
import { useId, useMemo, useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";
import { NODE_SERVICE_LABELS, type NodeServices, summarizeServices } from "@/lib/node-services";
import { useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

import { SettingsGroupHeader } from "../settings-group-header";
import { COUNTRY_CODES } from "./countries";

type Role = "" | "main" | "reserve" | "hidden";
type Presets = { russiaDirect: boolean; blockAds: boolean; blockQuic: boolean };
type HostOverride = { country?: string; role?: Role; exitNode?: string };
type Settings = {
  enabled: boolean;
  version: number;
  linkBase: string;
  effectiveLinkBase?: string;
  probeUrl: string;
  probeIntervalSeconds: number;
  autoName: string;
  reserveName: string;
  defaults: Presets;
  customRules: boolean;
  maxRules: number;
  directDomains: string[];
  blockDomains: string[];
  hosts: Record<string, HostOverride>;
};
type Host = {
  uuid: string;
  remark: string;
  address: string;
  port: number;
  detectedCountry: string;
  country: string;
  role: "main" | "reserve" | "hidden";
  disabled: boolean;
  hidden: boolean;
  down: boolean;
  nodes?: string[];
  /** The node the host exits from: the operator's choice or its only node. */
  exitNode?: string;
  services?: NodeServices;
  checkedAt?: string;
};
type PanelNode = { uuid: string; name: string };
/** The exit-node choice that keeps the host's own node. */
const OWN_NODE = "own";

const PRESETS: (keyof Presets)[] = ["russiaDirect", "blockAds", "blockQuic"];
const ROLES = ["main", "reserve", "hidden"] as const;
const ENDPOINT = "/v1/panel/settings/smart-subscription";

/**
 * The smart subscription's settings: the switch, what customers start with
 * and may change, and the hosts — the country read from each remark and the
 * role the operator gives it.
 */
export function SmartSubscriptionSettings() {
  const { data, isLoading, mutate } = useSWR<Settings, ApiError>(ENDPOINT, fetcher);
  return (
    <div className="flex flex-col gap-5">
      <SettingsGroupHeader group="smartSubscription" />
      {isLoading || !data ? (
        <Skeleton className="h-64 w-full" />
      ) : (
        <SettingsForm key={data.version} onSaved={() => void mutate()} settings={data} />
      )}
    </div>
  );
}

function SettingsForm({ onSaved, settings }: { onSaved: () => void; settings: Settings }) {
  const translate = useTranslations("admin.smartSubscription");
  const { can } = useSession();
  const editable = can("settings.write");
  const [draft, setDraft] = useState<Settings>(settings);
  const [domains, setDomains] = useState(settings.directDomains.join("\n"));
  const [blocked, setBlocked] = useState((settings.blockDomains ?? []).join("\n"));
  const [maxRules, setMaxRules] = useState(String(settings.maxRules));
  const [probeInterval, setProbeInterval] = useState(String(settings.probeIntervalSeconds));
  const { run, pending, error } = useOperatorAction();
  const ids = {
    link: useId(),
    domains: useId(),
    blocked: useId(),
    maxRules: useId(),
    auto: useId(),
    reserve: useId(),
    probe: useId(),
    interval: useId(),
  };

  async function save() {
    // effectiveLinkBase is read-only and the API refuses fields it does not
    // accept, so it is left out of what is sent back.
    const { effectiveLinkBase: _readOnly, ...sendable } = draft;
    const body: Omit<Settings, "effectiveLinkBase"> = {
      ...sendable,
      directDomains: lines(domains),
      blockDomains: lines(blocked),
      maxRules: Number(maxRules) || 0,
      probeIntervalSeconds: Number(probeInterval) || 0,
    };
    if (await run(ENDPOINT, { method: "PUT", body })) {
      toast.success(translate("saved"));
      onSaved();
    }
  }

  const field = (error?.problem as { field?: string } | null)?.field;

  return (
    <div className="flex flex-col gap-5">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center justify-between gap-3">
            {translate("moduleTitle")}
            <Switch
              aria-label={translate("enabled")}
              checked={draft.enabled}
              disabled={!editable}
              onCheckedChange={(enabled) => setDraft((current) => ({ ...current, enabled }))}
            />
          </CardTitle>
          <CardDescription>{translate("moduleDescription")}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={ids.link}>{translate("linkBase")}</Label>
            <Input
              aria-invalid={field === "linkBase"}
              id={ids.link}
              onChange={(event) =>
                setDraft((current) => ({ ...current, linkBase: event.target.value }))
              }
              placeholder={settings.effectiveLinkBase || "https://sub.example.com/v1/sub/"}
              readOnly={!editable}
              value={draft.linkBase}
            />
            <p className="text-muted-foreground text-xs">{translate("linkBaseHint")}</p>
          </div>
          {!settings.effectiveLinkBase && (
            <p className="flex items-start gap-2 rounded-md bg-warning/10 p-3 text-sm text-warning-tint-foreground">
              <TriangleAlert aria-hidden className="mt-0.5 size-4 shrink-0" />
              {translate("noLinkBase")}
            </p>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{translate("customersTitle")}</CardTitle>
          <CardDescription>{translate("customersDescription")}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          {PRESETS.map((preset) => (
            <SwitchRow
              checked={draft.defaults[preset]}
              disabled={!editable}
              hint={translate(`presets.${preset}.hint`)}
              key={preset}
              label={translate(`presets.${preset}.label`)}
              onChange={(on) =>
                setDraft((current) => ({
                  ...current,
                  defaults: { ...current.defaults, [preset]: on },
                }))
              }
            />
          ))}
          <SwitchRow
            checked={draft.customRules}
            disabled={!editable}
            hint={translate("customRulesHint")}
            label={translate("customRules")}
            onChange={(customRules) => setDraft((current) => ({ ...current, customRules }))}
          />
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={ids.maxRules}>{translate("maxRules")}</Label>
            <Input
              aria-invalid={field === "maxRules"}
              className="w-32"
              id={ids.maxRules}
              inputMode="numeric"
              onChange={(event) => setMaxRules(event.target.value)}
              readOnly={!editable}
              value={maxRules}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={ids.domains}>{translate("directDomains")}</Label>
            <Textarea
              aria-invalid={field === "directDomains"}
              id={ids.domains}
              onChange={(event) => setDomains(event.target.value)}
              placeholder={"bank.example\nmy-shop.example"}
              readOnly={!editable}
              rows={4}
              value={domains}
            />
            <p className="text-muted-foreground text-xs">{translate("directDomainsHint")}</p>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={ids.blocked}>{translate("blockDomains")}</Label>
            <Textarea
              aria-invalid={field === "blockDomains"}
              id={ids.blocked}
              onChange={(event) => setBlocked(event.target.value)}
              placeholder="ads.example"
              readOnly={!editable}
              rows={3}
              value={blocked}
            />
            <p className="text-muted-foreground text-xs">{translate("blockDomainsHint")}</p>
          </div>
        </CardContent>
      </Card>

      <HostsCard
        editable={editable}
        hosts={draft.hosts}
        onChange={(hosts) => setDraft((current) => ({ ...current, hosts }))}
      />

      <Card>
        <CardHeader>
          <CardTitle>{translate("advancedTitle")}</CardTitle>
          <CardDescription>{translate("advancedDescription")}</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={ids.auto}>{translate("autoName")}</Label>
            <Input
              id={ids.auto}
              maxLength={40}
              onChange={(event) =>
                setDraft((current) => ({ ...current, autoName: event.target.value }))
              }
              placeholder={translate("autoNamePlaceholder")}
              readOnly={!editable}
              value={draft.autoName}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={ids.reserve}>{translate("reserveName")}</Label>
            <Input
              id={ids.reserve}
              maxLength={40}
              onChange={(event) =>
                setDraft((current) => ({ ...current, reserveName: event.target.value }))
              }
              placeholder={translate("reserveNamePlaceholder")}
              readOnly={!editable}
              value={draft.reserveName}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={ids.probe}>{translate("probeUrl")}</Label>
            <Input
              aria-invalid={field === "probeUrl"}
              id={ids.probe}
              onChange={(event) =>
                setDraft((current) => ({ ...current, probeUrl: event.target.value }))
              }
              readOnly={!editable}
              value={draft.probeUrl}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={ids.interval}>{translate("probeInterval")}</Label>
            <Input
              aria-invalid={field === "probeIntervalSeconds"}
              id={ids.interval}
              inputMode="numeric"
              onChange={(event) => setProbeInterval(event.target.value)}
              readOnly={!editable}
              value={probeInterval}
            />
          </div>
        </CardContent>
      </Card>

      {error && (
        <p className="text-destructive text-sm" role="alert">
          {field
            ? translate("fieldError", {
                field: translate(`fields.${field}`),
                detail: error.message,
              })
            : error.message}
        </p>
      )}
      {editable && (
        <div className="flex justify-end">
          <Button disabled={pending} onClick={() => void save()}>
            {translate("save")}
          </Button>
        </div>
      )}
    </div>
  );
}

/** One domain per line, or separated by spaces or commas. */
function lines(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((domain) => domain.trim())
    .filter(Boolean);
}

function SwitchRow({
  checked,
  disabled,
  hint,
  label,
  onChange,
}: {
  checked: boolean;
  disabled: boolean;
  hint: string;
  label: string;
  onChange: (on: boolean) => void;
}) {
  const id = useId();
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="flex flex-col gap-0.5">
        <Label htmlFor={id}>{label}</Label>
        <p className="text-muted-foreground text-xs">{hint}</p>
      </div>
      <Switch checked={checked} disabled={disabled} id={id} onCheckedChange={onChange} />
    </div>
  );
}

function useCountryName() {
  const locale = useLocale();
  return useMemo(() => {
    let names: Intl.DisplayNames | null = null;
    try {
      names = new Intl.DisplayNames([locale], { type: "region" });
    } catch {
      names = null;
    }
    return (code: string) => {
      try {
        return names?.of(code) ?? code;
      } catch {
        return code;
      }
    };
  }, [locale]);
}

/**
 * Which node the host's traffic leaves from. A host whose inbound forwards to
 * another node exits there, and that node's geocheck is the one that counts.
 */
function ExitNodePicker({
  disabled,
  host,
  nodes,
  onChange,
  value,
}: {
  disabled: boolean;
  host: Host;
  nodes: PanelNode[];
  onChange: (exitNode: string) => void;
  value: string;
}) {
  const translate = useTranslations("admin.smartSubscription");
  const names = new Map(nodes.map((node) => [node.uuid, node.name]));
  const own = (host.nodes ?? []).map((id) => names.get(id) ?? id).join(", ");
  return (
    <Select
      disabled={disabled}
      onValueChange={(next) => onChange(next === OWN_NODE ? "" : next)}
      value={value || OWN_NODE}
    >
      <SelectTrigger aria-label={translate("exitNode")} className="w-[190px]">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={OWN_NODE}>
          {own ? translate("exitNodeOwn", { node: own }) : translate("exitNodeUnknown")}
        </SelectItem>
        {nodes.map((node) => (
          <SelectItem key={node.uuid} value={node.uuid}>
            {node.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

/** What the exit node's latest geocheck found, read-only. */
function NodeCheck({ checkedAt, services }: { checkedAt?: string; services?: NodeServices }) {
  const translate = useTranslations("admin.smartSubscription");
  const locale = useLocale();
  const summary = summarizeServices([{ services }]);
  if (!checkedAt || summary.length === 0) {
    return <span className="text-muted-foreground text-xs">{translate("notChecked")}</span>;
  }
  return (
    <div className="flex min-w-[260px] flex-col gap-1">
      <div className="flex flex-wrap gap-1">
        {summary.map(({ region, service, state }) => (
          <Badge
            className={cn(state === "no" && "line-through")}
            key={service}
            title={region ? `${NODE_SERVICE_LABELS[service]} · ${region}` : undefined}
            variant={state === "yes" ? "success" : state === "no" ? "danger" : "warning"}
          >
            {state === "yes" ? (
              <Check aria-hidden />
            ) : state === "no" ? (
              <X aria-hidden />
            ) : (
              <Minus aria-hidden />
            )}
            {NODE_SERVICE_LABELS[service]}
            {region && state !== "yes" ? ` · ${region}` : ""}
          </Badge>
        ))}
      </div>
      <span className="text-muted-foreground text-xs">
        {translate("checkedAt", {
          time: new Date(checkedAt).toLocaleString(locale, {
            dateStyle: "short",
            timeStyle: "short",
          }),
        })}
      </span>
    </div>
  );
}

function HostsCard({
  editable,
  hosts,
  onChange,
}: {
  editable: boolean;
  hosts: Record<string, HostOverride>;
  onChange: (hosts: Record<string, HostOverride>) => void;
}) {
  const translate = useTranslations("admin.smartSubscription");
  const { data, error, isLoading } = useSWR<{ hosts: Host[]; nodes?: PanelNode[] }, ApiError>(
    `${ENDPOINT}/hosts`,
    fetcher,
    {
      revalidateOnFocus: false,
    },
  );

  function update(uuid: string, change: HostOverride) {
    const next = { ...hosts[uuid], ...change };
    if (!next.exitNode) {
      delete next.exitNode;
    }
    const copy = { ...hosts };
    if (!next.country && (!next.role || next.role === "main") && !next.exitNode) {
      delete copy[uuid];
    } else {
      copy[uuid] = next;
    }
    onChange(copy);
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("hostsTitle")}</CardTitle>
        <CardDescription>{translate("hostsDescription")}</CardDescription>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <Skeleton className="h-32 w-full" />
        ) : error || !data ? (
          <p className="text-destructive text-sm">{translate("hostsUnavailable")}</p>
        ) : data.hosts.length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("hostsEmpty")}</p>
        ) : (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("host")}</TableHead>
                  <TableHead>{translate("country")}</TableHead>
                  <TableHead>{translate("status")}</TableHead>
                  <TableHead>{translate("role")}</TableHead>
                  <TableHead>{translate("exitNode")}</TableHead>
                  <TableHead>{translate("services")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.hosts.map((host) => {
                  const override = hosts[host.uuid] ?? {};
                  const role = override.role || "main";
                  return (
                    <TableRow key={host.uuid}>
                      <TableCell className="max-w-[260px]">
                        <div className="truncate font-medium">{host.remark}</div>
                        <div className="truncate font-mono text-muted-foreground text-xs">
                          {host.address}:{host.port}
                        </div>
                      </TableCell>
                      <TableCell>
                        <CountryPicker
                          detected={host.detectedCountry}
                          disabled={!editable}
                          onChange={(country) => update(host.uuid, { country })}
                          value={override.country ?? ""}
                        />
                      </TableCell>
                      <TableCell>
                        {host.disabled ? (
                          <Badge>{translate("statusDisabled")}</Badge>
                        ) : host.down ? (
                          <Badge variant="danger">{translate("statusDown")}</Badge>
                        ) : (
                          <Badge variant="success">{translate("statusUp")}</Badge>
                        )}
                      </TableCell>
                      <TableCell>
                        <Select
                          disabled={!editable}
                          onValueChange={(value) => update(host.uuid, { role: value as Role })}
                          value={role}
                        >
                          <SelectTrigger aria-label={translate("role")} className="w-[200px]">
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            {ROLES.map((option) => (
                              <SelectItem key={option} value={option}>
                                {translate(`roles.${option}`)}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      </TableCell>
                      <TableCell>
                        <ExitNodePicker
                          disabled={!editable}
                          host={host}
                          nodes={data.nodes ?? []}
                          onChange={(exitNode) => update(host.uuid, { exitNode })}
                          value={override.exitNode ?? ""}
                        />
                      </TableCell>
                      <TableCell>
                        <NodeCheck checkedAt={host.checkedAt} services={host.services} />
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
            <p className="mt-3 text-muted-foreground text-xs">{translate("servicesHint")}</p>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

/** A searchable country picker; empty keeps the country read from the remark. */
function CountryPicker({
  detected,
  disabled,
  onChange,
  value,
}: {
  detected: string;
  disabled: boolean;
  onChange: (country: string) => void;
  value: string;
}) {
  const translate = useTranslations("admin.smartSubscription");
  const name = useCountryName();
  const [open, setOpen] = useState(false);
  const label = value
    ? `${value} · ${name(value)}`
    : detected
      ? translate("countryDetected", { country: `${detected} · ${name(detected)}` })
      : translate("countryUnknown");

  return (
    <Popover onOpenChange={setOpen} open={open}>
      <PopoverTrigger asChild>
        <Button
          aria-expanded={open}
          aria-label={translate("country")}
          className={cn("w-[220px] justify-between font-normal", !value && "text-muted-foreground")}
          disabled={disabled}
          role="combobox"
          size="sm"
          variant="outline"
        >
          <span className="truncate">{label}</span>
          <ChevronsUpDown aria-hidden className="size-3.5 opacity-60" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-[260px] p-0">
        <Command>
          <CommandInput placeholder={translate("countrySearch")} />
          <CommandList>
            <CommandEmpty>{translate("countryNone")}</CommandEmpty>
            <CommandGroup>
              <CommandItem
                onSelect={() => {
                  onChange("");
                  setOpen(false);
                }}
                value="__auto"
              >
                <Check aria-hidden className={cn("size-4", value ? "opacity-0" : "opacity-100")} />
                {translate("countryAuto")}
              </CommandItem>
              {COUNTRY_CODES.map((code) => (
                <CommandItem
                  key={code}
                  onSelect={() => {
                    onChange(code);
                    setOpen(false);
                  }}
                  value={`${code} ${name(code)}`}
                >
                  <Check
                    aria-hidden
                    className={cn("size-4", value === code ? "opacity-100" : "opacity-0")}
                  />
                  {code} · {name(code)}
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
