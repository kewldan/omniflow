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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@omniflow/ui/tabs";
import { toast } from "@omniflow/ui/toast";
import {
  ArrowDown,
  ArrowUp,
  Check,
  ChevronsUpDown,
  Gauge,
  Globe,
  House,
  ImageUp,
  Lock,
  Magnet,
  Megaphone,
  Minus,
  Pencil,
  Plus,
  Search,
  Sparkles,
  Trash2,
  TriangleAlert,
  X,
} from "lucide-react";
import { useSearchParams } from "next/navigation";
import { useLocale, useTranslations } from "next-intl";
import { type ChangeEvent, useId, useMemo, useRef, useState } from "react";
import useSWR from "swr";

import { ApiError, apiFetch, fetcher } from "@/lib/api";
import { NODE_SERVICE_LABELS, type NodeServices, summarizeServices } from "@/lib/node-services";
import { useOperatorAction } from "@/lib/operations";
import { ServiceLogo } from "@/lib/service-logos";
import { useSession } from "@/lib/session";

import { SettingsGroupHeader } from "../settings-group-header";
import { COUNTRY_CODES } from "./countries";

type Role = "" | "main" | "reserve" | "hidden";
type HostOverride = { country?: string; role?: Role; exitNode?: string };
type Action = "direct" | "proxy" | "block";
type RuleKind = "domains" | "russia" | "ai" | "torrents" | "ads" | "quic";
type RuleMode = "locked" | "on" | "off" | "disabled";
type ServiceRule = {
  id: string;
  kind: RuleKind;
  name: string;
  caption: string;
  icon: string;
  action: Action;
  mode: RuleMode;
  domains: string[];
};
type Settings = {
  enabled: boolean;
  version: number;
  linkBase: string;
  effectiveLinkBase?: string;
  probeUrl: string;
  probeIntervalSeconds: number;
  autoName: string;
  reserveName: string;
  customRules: boolean;
  maxRules: number;
  rules: ServiceRule[];
  outageNotices: boolean;
  outageMinutes: number;
  whitelistMode: boolean;
  logoSearch?: boolean;
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
type Count = { key: string; count: number };
type Stats = {
  customized: number;
  hidden: Count[];
  autoExcluded: Count[];
  rules: { id: string; on: number; off: number }[];
  pinned: Count[];
  whitelist: number;
  reserveOff: number;
  domains: { domain: string; action: string; count: number }[];
};
/** The exit-node choice that keeps the host's own node. */
const OWN_NODE = "own";

const ROLES = ["main", "reserve", "hidden"] as const;
const MODES: RuleMode[] = ["locked", "on", "off", "disabled"];
/** What each kind of rule may do; the built-in ones carry their own matchers. */
const KIND_ACTIONS: Record<RuleKind, Action[]> = {
  ads: ["block"],
  ai: ["proxy"],
  domains: ["direct", "proxy", "block"],
  quic: ["block"],
  russia: ["direct"],
  torrents: ["direct", "block"],
};
const TABS = ["general", "rules", "hosts", "stats"] as const;
type Tab = (typeof TABS)[number];
const ENDPOINT = "/v1/panel/settings/smart-subscription";

/**
 * The smart subscription's settings: the switch and links, the service rules
 * customers see as cards, the hosts, and what customers chose.
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
  const params = useSearchParams();
  const initialTab = params.get("tab");
  const [tab, setTab] = useState<Tab>(
    TABS.includes(initialTab as Tab) ? (initialTab as Tab) : "general",
  );
  const [draft, setDraft] = useState<Settings>(settings);
  const [maxRules, setMaxRules] = useState(String(settings.maxRules));
  const [outageMinutes, setOutageMinutes] = useState(String(settings.outageMinutes));
  const [probeInterval, setProbeInterval] = useState(String(settings.probeIntervalSeconds));
  const { run, pending, error } = useOperatorAction();
  const ids = {
    link: useId(),
    maxRules: useId(),
    outage: useId(),
    auto: useId(),
    reserve: useId(),
    probe: useId(),
    interval: useId(),
  };

  async function save() {
    // The read-only fields are left out: the API refuses fields it does not
    // accept.
    const { effectiveLinkBase: _link, logoSearch: _logo, ...sendable } = draft;
    const body = {
      ...sendable,
      rules: draft.rules.map((rule) => ({ ...rule, domains: lines(rule.domains.join("\n")) })),
      maxRules: Number(maxRules) || 0,
      outageMinutes: Number(outageMinutes) || 0,
      probeIntervalSeconds: Number(probeInterval) || 0,
    };
    if (await run(ENDPOINT, { method: "PUT", body })) {
      toast.success(translate("saved"));
      onSaved();
    }
  }

  const field = (error?.problem as { field?: string } | null)?.field;
  const ruleIndex = field?.match(/^rules\[(\d+)\]/)?.[1];
  const fieldLabel = !field
    ? ""
    : ruleIndex !== undefined
      ? translate("fields.rule", { index: Number(ruleIndex) + 1 })
      : translate(`fields.${field}`);

  function addRuleFromDomain(domain: string, action: string) {
    const known = (["direct", "proxy", "block"] as const).find((value) => value === action);
    setDraft((current) => ({
      ...current,
      rules: [
        newRule({ action: known ?? "direct", domains: [domain], name: domain }),
        ...current.rules,
      ],
    }));
    setTab("rules");
  }

  return (
    <div className="flex flex-col gap-5">
      <Tabs onValueChange={(value) => setTab(value as Tab)} value={tab}>
        <TabsList>
          {TABS.map((value) => (
            <TabsTrigger key={value} value={value}>
              {translate(`tabs.${value}`)}
            </TabsTrigger>
          ))}
        </TabsList>

        <TabsContent className="flex flex-col gap-5" value="general">
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
              <SwitchRow
                checked={draft.whitelistMode}
                disabled={!editable}
                hint={translate("whitelistModeHint")}
                label={translate("whitelistMode")}
                onChange={(whitelistMode) => setDraft((current) => ({ ...current, whitelistMode }))}
              />
              <SwitchRow
                checked={draft.outageNotices}
                disabled={!editable}
                hint={translate("outageNoticesHint")}
                label={translate("outageNotices")}
                onChange={(outageNotices) => setDraft((current) => ({ ...current, outageNotices }))}
              />
              <div className="flex flex-col gap-1.5">
                <Label htmlFor={ids.outage}>{translate("outageMinutes")}</Label>
                <Input
                  aria-invalid={field === "outageMinutes"}
                  className="w-32"
                  disabled={!draft.outageNotices}
                  id={ids.outage}
                  inputMode="numeric"
                  onChange={(event) => setOutageMinutes(event.target.value)}
                  readOnly={!editable}
                  value={outageMinutes}
                />
                <p className="text-muted-foreground text-xs">{translate("outageMinutesHint")}</p>
              </div>
            </CardContent>
          </Card>

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
        </TabsContent>

        <TabsContent value="rules">
          <RulesCard
            editable={editable}
            logoSearch={settings.logoSearch ?? false}
            onChange={(rules) => setDraft((current) => ({ ...current, rules }))}
            rules={draft.rules}
          />
        </TabsContent>

        <TabsContent value="hosts">
          <HostsCard
            editable={editable}
            hosts={draft.hosts}
            onChange={(hosts) => setDraft((current) => ({ ...current, hosts }))}
          />
        </TabsContent>

        <TabsContent value="stats">
          <StatsCard editable={editable} onAddRule={addRuleFromDomain} rules={draft.rules} />
        </TabsContent>
      </Tabs>

      {error && (
        <p className="text-destructive text-sm" role="alert">
          {field
            ? translate("fieldError", { field: fieldLabel, detail: error.message })
            : error.message}
        </p>
      )}
      {editable && tab !== "stats" && (
        <div className="sticky bottom-4 flex justify-end">
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

function newRule(seed: Partial<ServiceRule> = {}): ServiceRule {
  const suffix = Math.random().toString(36).slice(2, 8);
  return {
    action: "direct",
    caption: "",
    domains: [],
    icon: "",
    id: `rule-${suffix}`,
    kind: "domains",
    mode: "locked",
    name: "",
    ...seed,
  };
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
            <ServiceLogo className="size-3" service={service} />
            {NODE_SERVICE_LABELS[service]}
            {state === "no" ? (
              <X aria-hidden className="size-2.5 opacity-70" />
            ) : state !== "yes" ? (
              <Minus aria-hidden className="size-2.5 opacity-70" />
            ) : null}
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

const BUILTIN_ICONS: Record<Exclude<RuleKind, "domains">, typeof Globe> = {
  ads: Megaphone,
  ai: Sparkles,
  quic: Gauge,
  russia: House,
  torrents: Magnet,
};

function RuleIcon({ rule }: { rule: ServiceRule }) {
  if (rule.icon) {
    return (
      // biome-ignore lint/performance/noImgElement: a small icon the API serves; next/image adds nothing here.
      <img
        alt=""
        className="size-9 shrink-0 rounded-md border border-border bg-white object-contain p-1"
        height={36}
        src={`${ENDPOINT}/icons/${encodeURIComponent(rule.icon)}`}
        width={36}
      />
    );
  }
  const Icon = rule.kind === "domains" ? Globe : BUILTIN_ICONS[rule.kind];
  return (
    <span className="flex size-9 shrink-0 items-center justify-center rounded-md bg-secondary">
      <Icon aria-hidden className="size-[18px]" />
    </span>
  );
}

/** The panel's own name and caption for a built-in rule. */
function useBuiltinText() {
  const translate = useTranslations("account.smart.builtin");
  return (rule: ServiceRule) => {
    if (rule.kind === "domains") {
      return { caption: "", name: "" };
    }
    const kind = rule.kind === "torrents" ? `torrents_${rule.action}` : rule.kind;
    return { caption: translate(`${kind}.caption`), name: translate(`${kind}.name`) };
  };
}

/**
 * The service rules as the operator edits them, in the order customers see
 * them. Each is a card on the customer's Routing tab, grouped by what it does.
 */
function RulesCard({
  editable,
  logoSearch,
  onChange,
  rules,
}: {
  editable: boolean;
  logoSearch: boolean;
  onChange: (rules: ServiceRule[]) => void;
  rules: ServiceRule[];
}) {
  const translate = useTranslations("admin.smartSubscription");
  const builtin = useBuiltinText();
  const [open, setOpen] = useState<string | null>(null);

  function update(index: number, change: Partial<ServiceRule>) {
    onChange(rules.map((rule, at) => (at === index ? { ...rule, ...change } : rule)));
  }

  function move(index: number, by: number) {
    const target = index + by;
    if (target < 0 || target >= rules.length) {
      return;
    }
    const copy = [...rules];
    const [rule] = copy.splice(index, 1);
    copy.splice(target, 0, rule as ServiceRule);
    onChange(copy);
  }

  function add() {
    const rule = newRule();
    onChange([rule, ...rules]);
    setOpen(rule.id);
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center justify-between gap-3">
          {translate("rulesTitle")}
          {editable && (
            <Button onClick={add} size="sm" variant="outline">
              <Plus aria-hidden />
              {translate("addRule")}
            </Button>
          )}
        </CardTitle>
        <CardDescription>{translate("rulesDescription")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col">
        {rules.map((rule, index) => {
          const fallback = builtin(rule);
          const name = rule.name || fallback.name || translate("ruleUnnamed");
          const expanded = open === rule.id;
          return (
            <div className="border-border border-t py-3 first:border-t-0" key={rule.id}>
              <div className="flex items-center gap-3">
                <RuleIcon rule={rule} />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-1.5">
                    <span className="truncate font-medium text-sm">{name}</span>
                    {rule.mode === "locked" && (
                      <Lock aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />
                    )}
                  </div>
                  <div className="flex flex-wrap items-center gap-1.5 text-muted-foreground text-xs">
                    <Badge
                      variant={
                        rule.action === "block"
                          ? "danger"
                          : rule.action === "proxy"
                            ? "info"
                            : "neutral"
                      }
                    >
                      {translate(`ruleActions.${rule.action}`)}
                    </Badge>
                    <span>
                      {rule.kind === "domains"
                        ? translate("ruleDomainCount", {
                            count: lines(rule.domains.join("\n")).length,
                          })
                        : translate("ruleBuiltin")}
                    </span>
                  </div>
                </div>
                <Select
                  disabled={!editable}
                  onValueChange={(mode) => update(index, { mode: mode as RuleMode })}
                  value={rule.mode}
                >
                  <SelectTrigger aria-label={translate("ruleMode")} className="w-[190px]">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {MODES.map((mode) => (
                      <SelectItem key={mode} value={mode}>
                        {translate(`ruleModes.${mode}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <div className="flex items-center">
                  <Button
                    aria-label={translate("moveUp")}
                    disabled={!editable || index === 0}
                    onClick={() => move(index, -1)}
                    size="icon-sm"
                    variant="ghost"
                  >
                    <ArrowUp aria-hidden />
                  </Button>
                  <Button
                    aria-label={translate("moveDown")}
                    disabled={!editable || index === rules.length - 1}
                    onClick={() => move(index, 1)}
                    size="icon-sm"
                    variant="ghost"
                  >
                    <ArrowDown aria-hidden />
                  </Button>
                  <Button
                    aria-expanded={expanded}
                    aria-label={translate("editRule", { name })}
                    onClick={() => setOpen(expanded ? null : rule.id)}
                    size="icon-sm"
                    variant={expanded ? "secondary" : "ghost"}
                  >
                    <Pencil aria-hidden />
                  </Button>
                  {rule.kind === "domains" && editable && (
                    <Button
                      aria-label={translate("deleteRule", { name })}
                      onClick={() => onChange(rules.filter((_, at) => at !== index))}
                      size="icon-sm"
                      variant="ghost"
                    >
                      <Trash2 aria-hidden />
                    </Button>
                  )}
                </div>
              </div>
              {expanded && (
                <RuleEditor
                  editable={editable}
                  fallback={fallback}
                  logoSearch={logoSearch}
                  onChange={(change) => update(index, change)}
                  rule={rule}
                />
              )}
            </div>
          );
        })}
        <p className="mt-3 text-muted-foreground text-xs">{translate("rulesHint")}</p>
      </CardContent>
    </Card>
  );
}

function RuleEditor({
  editable,
  fallback,
  logoSearch,
  onChange,
  rule,
}: {
  editable: boolean;
  fallback: { name: string; caption: string };
  logoSearch: boolean;
  onChange: (change: Partial<ServiceRule>) => void;
  rule: ServiceRule;
}) {
  const translate = useTranslations("admin.smartSubscription");
  const ids = { name: useId(), caption: useId(), domains: useId(), action: useId() };
  const actions = KIND_ACTIONS[rule.kind];
  return (
    <div className="mt-3 grid gap-4 rounded-md bg-secondary/50 p-4 sm:grid-cols-2">
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={ids.name}>{translate("ruleName")}</Label>
        <Input
          id={ids.name}
          maxLength={60}
          onChange={(event) => onChange({ name: event.target.value })}
          placeholder={fallback.name || translate("ruleNamePlaceholder")}
          readOnly={!editable}
          value={rule.name}
        />
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={ids.action}>{translate("ruleAction")}</Label>
        <Select
          disabled={!editable || actions.length < 2}
          onValueChange={(action) => onChange({ action: action as Action })}
          value={rule.action}
        >
          <SelectTrigger id={ids.action}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {actions.map((action) => (
              <SelectItem key={action} value={action}>
                {translate(`ruleActions.${action}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="flex flex-col gap-1.5 sm:col-span-2">
        <Label htmlFor={ids.caption}>{translate("ruleCaption")}</Label>
        <Textarea
          id={ids.caption}
          maxLength={160}
          onChange={(event) => onChange({ caption: event.target.value })}
          placeholder={fallback.caption || translate("ruleCaptionPlaceholder")}
          readOnly={!editable}
          rows={2}
          value={rule.caption}
        />
      </div>
      {rule.kind === "domains" && (
        <div className="flex flex-col gap-1.5 sm:col-span-2">
          <Label htmlFor={ids.domains}>{translate("ruleDomains")}</Label>
          <Textarea
            id={ids.domains}
            onChange={(event) => onChange({ domains: event.target.value.split("\n") })}
            placeholder={"gosuslugi.ru\nesia.gosuslugi.ru"}
            readOnly={!editable}
            rows={4}
            value={rule.domains.join("\n")}
          />
          <p className="text-muted-foreground text-xs">{translate("ruleDomainsHint")}</p>
        </div>
      )}
      <IconPicker
        domain={lines(rule.domains.join("\n"))[0] ?? ""}
        editable={editable}
        icon={rule.icon}
        logoSearch={logoSearch}
        onChange={(icon) => onChange({ icon })}
      />
    </div>
  );
}

/**
 * The rule's icon: an upload, or a site's logo fetched once through logo.dev
 * and stored, so customers' browsers never ask a third party for it.
 */
function IconPicker({
  domain,
  editable,
  icon,
  logoSearch,
  onChange,
}: {
  domain: string;
  editable: boolean;
  icon: string;
  logoSearch: boolean;
  onChange: (icon: string) => void;
}) {
  const translate = useTranslations("admin.smartSubscription");
  const file = useRef<HTMLInputElement>(null);
  const [lookup, setLookup] = useState(domain);
  const [busy, setBusy] = useState(false);

  async function store(request: Promise<{ id: string }>) {
    setBusy(true);
    try {
      const { id } = await request;
      onChange(id);
    } catch (problem) {
      toast.error(problem instanceof ApiError ? problem.message : translate("iconFailed"));
    } finally {
      setBusy(false);
    }
  }

  function upload(event: ChangeEvent<HTMLInputElement>) {
    const chosen = event.target.files?.[0];
    event.target.value = "";
    if (!chosen) {
      return;
    }
    void store(
      apiFetch<{ id: string }>(`${ENDPOINT}/icons`, {
        body: chosen,
        headers: { "Content-Type": "application/octet-stream" },
        method: "POST",
      }),
    );
  }

  function fetchLogo() {
    void store(
      apiFetch<{ id: string }>(`${ENDPOINT}/icons/fetch`, {
        body: JSON.stringify({ domain: lookup || domain }),
        method: "POST",
      }),
    );
  }

  return (
    <div className="flex flex-col gap-1.5 sm:col-span-2">
      <Label>{translate("ruleIcon")}</Label>
      <div className="flex flex-wrap items-center gap-2">
        {icon ? (
          // biome-ignore lint/performance/noImgElement: a small icon the API serves; next/image adds nothing here.
          <img
            alt=""
            className="size-10 rounded-md border border-border bg-white object-contain p-1"
            height={40}
            src={`${ENDPOINT}/icons/${encodeURIComponent(icon)}`}
            width={40}
          />
        ) : (
          <span className="flex size-10 items-center justify-center rounded-md border border-border border-dashed text-muted-foreground">
            <Globe aria-hidden className="size-4" />
          </span>
        )}
        {editable && (
          <>
            <input
              accept="image/png,image/jpeg,image/webp,image/svg+xml"
              className="hidden"
              onChange={upload}
              ref={file}
              type="file"
            />
            <Button
              disabled={busy}
              onClick={() => file.current?.click()}
              size="sm"
              variant="outline"
            >
              <ImageUp aria-hidden />
              {translate("iconUpload")}
            </Button>
            {logoSearch && (
              <span className="flex items-center gap-1">
                <Input
                  aria-label={translate("iconDomain")}
                  className="h-8 w-44"
                  onChange={(event) => setLookup(event.target.value)}
                  placeholder={domain || "gosuslugi.ru"}
                  value={lookup}
                />
                <Button
                  disabled={busy || !(lookup || domain)}
                  onClick={fetchLogo}
                  size="sm"
                  variant="outline"
                >
                  <Search aria-hidden />
                  {translate("iconFetch")}
                </Button>
              </span>
            )}
            {icon && (
              <Button disabled={busy} onClick={() => onChange("")} size="sm" variant="ghost">
                <X aria-hidden />
                {translate("iconRemove")}
              </Button>
            )}
          </>
        )}
      </div>
      <p className="text-muted-foreground text-xs">
        {logoSearch ? translate("iconHint") : translate("iconHintNoSearch")}
      </p>
    </div>
  );
}

/**
 * What customers did with the module, in aggregate. A customer's own domain
 * shows up only once three subscriptions share it.
 */
function StatsCard({
  editable,
  onAddRule,
  rules,
}: {
  editable: boolean;
  onAddRule: (domain: string, action: string) => void;
  rules: ServiceRule[];
}) {
  const translate = useTranslations("admin.smartSubscription");
  const builtin = useBuiltinText();
  const countryName = useCountryName();
  const { data, error, isLoading } = useSWR<Stats, ApiError>(`${ENDPOINT}/stats`, fetcher);
  const { data: hosts } = useSWR<{ hosts: Host[] }, ApiError>(`${ENDPOINT}/hosts`, fetcher, {
    revalidateOnFocus: false,
  });
  if (isLoading) {
    return <Skeleton className="h-64 w-full" />;
  }
  if (error || !data) {
    return <p className="text-destructive text-sm">{translate("statsUnavailable")}</p>;
  }
  const remark = new Map(
    (hosts?.hosts ?? []).map((host) => [host.uuid.toLowerCase(), host.remark]),
  );
  const ruleName = (id: string) => {
    const rule = rules.find((candidate) => candidate.id === id);
    if (!rule) {
      return id;
    }
    return rule.name || builtin(rule).name || id;
  };
  const tiles = [
    { label: translate("statsCustomized"), value: data.customized },
    { label: translate("statsWhitelist"), value: data.whitelist },
    { label: translate("statsReserveOff"), value: data.reserveOff },
    {
      label: translate("statsPinned"),
      value: data.pinned.reduce((sum, item) => sum + item.count, 0),
    },
  ];

  return (
    <div className="flex flex-col gap-5">
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {tiles.map((tile) => (
          <Card className="p-4" key={tile.label}>
            <div className="text-muted-foreground text-xs">{tile.label}</div>
            <div className="font-semibold text-2xl tabular-nums">{tile.value}</div>
          </Card>
        ))}
      </div>

      <div className="grid gap-5 lg:grid-cols-2">
        <StatsList
          empty={translate("statsNone")}
          items={data.rules.map((rule) => ({
            key: rule.id,
            label: ruleName(rule.id),
            value: translate("statsRuleChoice", { off: rule.off, on: rule.on }),
          }))}
          title={translate("statsRules")}
        />
        <StatsList
          empty={translate("statsNone")}
          items={data.pinned.map((item) => ({
            key: item.key,
            label: remark.get(item.key.toLowerCase()) ?? item.key,
            value: String(item.count),
          }))}
          title={translate("statsPinnedHosts")}
        />
        <StatsList
          empty={translate("statsNone")}
          items={data.hidden.map((item) => ({
            key: item.key,
            label: countryName(item.key),
            value: String(item.count),
          }))}
          title={translate("statsHidden")}
        />
        <StatsList
          empty={translate("statsNone")}
          items={data.autoExcluded.map((item) => ({
            key: item.key,
            label: countryName(item.key),
            value: String(item.count),
          }))}
          title={translate("statsAutoExcluded")}
        />
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{translate("statsDomains")}</CardTitle>
          <CardDescription>{translate("statsDomainsHint")}</CardDescription>
        </CardHeader>
        <CardContent>
          {data.domains.length === 0 ? (
            <p className="text-muted-foreground text-sm">{translate("statsNone")}</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("statsDomain")}</TableHead>
                  <TableHead>{translate("ruleAction")}</TableHead>
                  <TableHead className="text-right">{translate("statsSubscriptions")}</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.domains.map((domain) => (
                  <TableRow key={domain.domain}>
                    <TableCell className="font-mono text-xs">{domain.domain}</TableCell>
                    <TableCell>
                      {(["direct", "proxy", "block"] as const).includes(domain.action as Action)
                        ? translate(`ruleActions.${domain.action as Action}`)
                        : domain.action}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{domain.count}</TableCell>
                    <TableCell className="text-right">
                      {editable && (
                        <Button
                          onClick={() => onAddRule(domain.domain, domain.action)}
                          size="sm"
                          variant="ghost"
                        >
                          <Plus aria-hidden />
                          {translate("statsMakeRule")}
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function StatsList({
  empty,
  items,
  title,
}: {
  empty: string;
  items: { key: string; label: string; value: string }[];
  title: string;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{title}</CardTitle>
      </CardHeader>
      <CardContent>
        {items.length === 0 ? (
          <p className="text-muted-foreground text-sm">{empty}</p>
        ) : (
          <ul className="flex flex-col gap-1.5 text-sm">
            {items.map((item) => (
              <li className="flex items-center justify-between gap-3" key={item.key}>
                <span className="min-w-0 truncate">{item.label}</span>
                <span className="shrink-0 text-muted-foreground tabular-nums">{item.value}</span>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}
