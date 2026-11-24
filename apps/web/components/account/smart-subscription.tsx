"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Input } from "@omniflow/ui/input";
import { cn } from "@omniflow/ui/lib/utils";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Switch } from "@omniflow/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@omniflow/ui/tabs";
import { toast } from "@omniflow/ui/toast";
import {
  Activity,
  ChevronDown,
  ChevronRight,
  Globe,
  Minus,
  Plus,
  ShieldHalf,
  X,
  Zap,
} from "lucide-react";
import Link from "next/link";
import { useLocale, useTranslations } from "next-intl";
import { type FormEvent, useEffect, useMemo, useState } from "react";
import useSWR from "swr";

import { AccountNotice, ListSkeleton } from "@/components/account/state";
import { ApiError, apiFetch, fetcher } from "@/lib/api";
import {
  NODE_SERVICE_LABELS,
  type NodeService,
  type NodeServices,
  type SummaryState,
  summarizeServices,
} from "@/lib/node-services";
import { ServiceLogo } from "@/lib/service-logos";

type Presets = { russiaDirect: boolean; blockAds: boolean; blockQuic: boolean };
type Action = "direct" | "proxy" | "block";
type Rule = { domain: string; action: Action };
type Preferences = {
  hiddenCountries: string[];
  autoExcluded: string[];
  reserve: boolean;
  presets: Presets;
  rules: Rule[];
};
type Server = {
  name: string;
  address: string;
  port: number;
  down: boolean;
  /** What the latest geocheck of the server's exit node found. */
  services?: NodeServices;
};
type SmartView = {
  servers: { countries: { code: string; servers: Server[] }[]; reserve: Server[] };
  preferences: Preferences;
  customized: boolean;
  defaults: Presets;
  customRules: boolean;
  maxRules: number;
};

/** Servers whose country could not be read are grouped under this code. */
const UNKNOWN = "ZZ";
const ACTIONS: Action[] = ["direct", "proxy", "block"];

/** Whether the operator turned the module on. */
function useSmartEnabled() {
  const { data } = useSWR<{ enabled: boolean }, ApiError>(
    "/v1/account/smart-subscription",
    fetcher,
    {
      // A module the installation does not run answers 404; that is "off".
      shouldRetryOnError: false,
    },
  );
  return data?.enabled ?? false;
}

/**
 * The way in from a subscription's page. It renders nothing while the
 * operator has the module off, or for a subscription not yet set up.
 */
export function SmartSubscriptionEntry({
  provisioned,
  subscriptionId,
}: {
  provisioned: boolean;
  subscriptionId: string;
}) {
  const translate = useTranslations("account.smart");
  const enabled = useSmartEnabled();
  if (!enabled || !provisioned) {
    return null;
  }
  return (
    <Link
      className="flex items-center gap-3 rounded-lg border border-border bg-card p-4 transition-colors hover:border-foreground/20"
      href={`/account/subscriptions/${subscriptionId}/servers`}
    >
      <span className="flex size-9 shrink-0 items-center justify-center rounded-md bg-secondary">
        <Globe aria-hidden className="size-[18px]" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block font-medium text-[14px]">{translate("entryTitle")}</span>
        <span className="block text-[12.5px] text-muted-foreground">
          {translate("entryDescription")}
        </span>
      </span>
      <ChevronRight aria-hidden className="size-4 text-subtle-foreground" />
    </Link>
  );
}

function flag(code: string): string {
  if (code.length !== 2 || code === UNKNOWN) {
    return "🌐";
  }
  return String.fromCodePoint(
    ...[...code.toUpperCase()].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65),
  );
}

function useCountryName() {
  const locale = useLocale();
  const translate = useTranslations("account.smart");
  return useMemo(() => {
    let names: Intl.DisplayNames | null = null;
    try {
      names = new Intl.DisplayNames([locale], { type: "region" });
    } catch {
      names = null;
    }
    return (code: string) => {
      if (code === UNKNOWN) {
        return translate("otherCountry");
      }
      try {
        return names?.of(code) ?? code;
      } catch {
        return code;
      }
    };
  }, [locale, translate]);
}

/**
 * How long the browser takes to open a connection to a server: an image
 * request to it fails once the TLS handshake meets a certificate that is not
 * for an image, and the time until it fails is about two round trips. The
 * content security policy already lets the customer panel load images from
 * any https origin, so no request goes anywhere a page could not already.
 */
function measure(server: Server, timeoutMs = 3000): Promise<number | null> {
  return new Promise((resolve) => {
    const host = server.address.includes(":") ? `[${server.address}]` : server.address;
    const port = server.port && server.port !== 443 ? `:${server.port}` : "";
    const image = new Image();
    const started = performance.now();
    const timer = window.setTimeout(() => {
      image.src = "";
      resolve(null);
    }, timeoutMs);
    const done = () => {
      window.clearTimeout(timer);
      resolve(Math.round(performance.now() - started));
    };
    image.onload = done;
    image.onerror = done;
    image.src = `https://${host}${port}/favicon.ico?ping=${Date.now()}`;
  });
}

/** The better of two attempts: the first also pays for the DNS lookup. */
async function ping(server: Server): Promise<number | null> {
  const first = await measure(server);
  if (first === null) {
    return null;
  }
  const second = await measure(server);
  return second === null ? first : Math.min(first, second);
}

function key(server: Server) {
  return `${server.address}:${server.port}`;
}

/** A measured ping; `inline` sits inside a line of text rather than in a column. */
function Ping({
  inline = false,
  measuring,
  value,
}: {
  inline?: boolean;
  measuring: boolean;
  value: number | null | undefined;
}) {
  const translate = useTranslations("account.smart");
  const shape = inline
    ? "font-mono text-[12px]"
    : "min-w-[52px] text-right font-mono text-[11.5px]";
  if (value === undefined) {
    return <span className={cn(shape, "text-subtle-foreground")}>{measuring ? "…" : "—"}</span>;
  }
  if (value === null) {
    return <span className={cn(shape, "text-destructive")}>{translate("noAnswer")}</span>;
  }
  const tone = value < 120 ? "text-success" : value < 250 ? "text-warning" : "text-destructive";
  return <span className={cn(shape, tone)}>{translate("ms", { value })}</span>;
}

/** The service's line for a badge's tooltip and screen readers. */
function useServiceTitle() {
  const translate = useTranslations("account.smart");
  return (service: NodeService, state: SummaryState, region?: string) => {
    const name = NODE_SERVICE_LABELS[service];
    if (state === "no" && service === "reddit") {
      return translate("serviceRedditNo");
    }
    if (state === "limited" && (service === "youtube" || service === "netflix")) {
      return translate(`serviceLimited.${service}`);
    }
    const line = translate(`serviceState.${state}`, { service: name });
    return region && state !== "some"
      ? `${line} · ${translate("serviceRegion", { region })}`
      : line;
  };
}

/**
 * One badge per service the checks settled: green opens, amber opens with
 * limits or on only some servers, struck out doesn't.
 */
function ServiceChips({ servers }: { servers: Server[] }) {
  const title = useServiceTitle();
  const summary = summarizeServices(servers);
  if (summary.length === 0) {
    return null;
  }
  return (
    <span className="mt-1 flex flex-wrap gap-1">
      {summary.map(({ region, service, state }) => {
        const line = title(service, state, region);
        return (
          <Badge
            aria-label={line}
            className={cn("px-1.5 font-normal", state === "no" && "line-through")}
            key={service}
            title={line}
            variant={state === "yes" ? "success" : state === "no" ? "neutral" : "warning"}
          >
            <ServiceLogo className="size-3" service={service} />
            {NODE_SERVICE_LABELS[service]}
            {state === "no" ? (
              <X aria-hidden className="size-2.5 opacity-70" />
            ) : state !== "yes" ? (
              <Minus aria-hidden className="size-2.5 opacity-70" />
            ) : null}
          </Badge>
        );
      })}
    </span>
  );
}

function toggle(list: string[], code: string, on: boolean): string[] {
  const without = list.filter((item) => item !== code);
  return on ? [...without, code].sort() : without;
}

/**
 * The customer's own subscription: which countries it carries, which ones
 * the fastest-server group may pick, the whitelist bypass, and routing.
 */
export function SmartSubscriptionScreen({ subscriptionId }: { subscriptionId: string }) {
  const translate = useTranslations("account.smart");
  const shared = useTranslations("account");
  const path = `/v1/account/subscriptions/${subscriptionId}/smart`;
  const { data, error, isLoading, mutate } = useSWR<SmartView, ApiError>(path, fetcher, {
    revalidateOnFocus: false,
  });
  const [draft, setDraft] = useState<Preferences | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (data) {
      setDraft(structuredClone(data.preferences));
    }
  }, [data]);

  if (isLoading || (data && !draft)) {
    return <ListSkeleton rows={3} />;
  }
  if (error || !data || !draft) {
    const off = error?.code === "smart_subscription_off";
    const unavailable = error?.status === 503;
    return (
      <AccountNotice
        description={
          off
            ? translate("offDescription")
            : unavailable
              ? translate("unavailableDescription")
              : shared("states.errorDescription")
        }
        title={off ? translate("offTitle") : shared("states.error")}
        variant={off ? "empty" : "danger"}
      />
    );
  }

  const dirty = JSON.stringify(draft) !== JSON.stringify(data.preferences);

  async function save() {
    if (!draft) {
      return;
    }
    setSaving(true);
    try {
      await apiFetch(path, { body: JSON.stringify(draft), method: "PUT" });
      await mutate();
      toast.success(translate("saved"));
    } catch (saveError) {
      toast.error(saveError instanceof ApiError ? saveError.message : shared("states.error"));
    } finally {
      setSaving(false);
    }
  }

  async function reset() {
    setSaving(true);
    try {
      await apiFetch(path, { method: "DELETE" });
      await mutate();
      toast.success(translate("resetDone"));
    } catch (resetError) {
      toast.error(resetError instanceof ApiError ? resetError.message : shared("states.error"));
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="animate-step-in space-y-4 pb-4">
      <h1 className="font-semibold text-[19px] tracking-[-0.02em]">{translate("title")}</h1>
      <Tabs defaultValue="servers">
        <TabsList className="w-full">
          <TabsTrigger className="flex-1" value="servers">
            {translate("tabServers")}
          </TabsTrigger>
          <TabsTrigger className="flex-1" value="routes">
            {translate("tabRoutes")}
          </TabsTrigger>
        </TabsList>
        <TabsContent value="servers">
          <ServersTab draft={draft} onChange={setDraft} view={data} />
        </TabsContent>
        <TabsContent value="routes">
          <RoutesTab draft={draft} onChange={setDraft} view={data} />
        </TabsContent>
      </Tabs>

      <div className="space-y-2">
        <div className="flex gap-2">
          <Button
            className="flex-1"
            disabled={!dirty || saving}
            onClick={() => void save()}
            size="lg"
          >
            {shared("actions.save")}
          </Button>
          {data.customized && (
            <Button disabled={saving} onClick={() => void reset()} size="lg" variant="outline">
              {translate("reset")}
            </Button>
          )}
        </div>
        <p className="px-1 text-[12.5px] text-muted-foreground">{translate("appliesOnRefresh")}</p>
      </div>
    </div>
  );
}

function ServersTab({
  draft,
  onChange,
  view,
}: {
  draft: Preferences;
  onChange: (next: Preferences) => void;
  view: SmartView;
}) {
  const translate = useTranslations("account.smart");
  const countryName = useCountryName();
  const [open, setOpen] = useState<string | null>(null);
  const [pings, setPings] = useState<Record<string, number | null>>({});
  const [measuring, setMeasuring] = useState(false);

  const hidden = new Set(draft.hiddenCountries);
  const excluded = new Set(draft.autoExcluded);
  const inAuto = view.servers.countries.filter(
    (c) => !hidden.has(c.code) && !excluded.has(c.code),
  ).length;
  const everyServer = [
    ...view.servers.countries.flatMap((c) => c.servers),
    ...view.servers.reserve,
  ];
  const checked = summarizeServices(everyServer).length > 0;

  async function measureAll() {
    setMeasuring(true);
    setPings({});
    await Promise.all(
      everyServer.map(async (server) => {
        const value = server.down ? null : await ping(server);
        setPings((current) => ({ ...current, [key(server)]: value }));
      }),
    );
    setMeasuring(false);
  }

  function best(servers: Server[]): number | null | undefined {
    const values = servers.map((server) => pings[key(server)]);
    if (values.some((value) => value === undefined)) {
      return undefined;
    }
    const answered = values.filter((value): value is number => typeof value === "number");
    return answered.length > 0 ? Math.min(...answered) : null;
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-3">
        <p className="text-[12.5px] text-muted-foreground">{translate("pingHint")}</p>
        <Button disabled={measuring} onClick={() => void measureAll()} size="sm" variant="outline">
          <Activity aria-hidden />
          {translate("measure")}
        </Button>
      </div>

      <div className="rounded-lg bg-secondary p-4">
        <div className="flex items-center gap-2">
          <Zap aria-hidden className="size-4" />
          <span className="flex-1 font-medium text-[14px]">{translate("autoTitle")}</span>
          <Badge>{translate("autoCountries", { count: inAuto })}</Badge>
        </div>
        <p className="mt-1 text-[12.5px] text-muted-foreground">{translate("autoDescription")}</p>
      </div>

      {checked && (
        <p className="px-1 text-[12.5px] text-muted-foreground">{translate("servicesHint")}</p>
      )}

      <section className="rounded-lg border border-border bg-card px-4">
        <div className="flex items-center gap-2 pt-3 pb-1 text-[11.5px] text-subtle-foreground">
          <span className="flex-1">{translate("countries")}</span>
          <span className="w-[52px] text-center">{translate("visible")}</span>
          <span className="w-[52px] text-center">{translate("auto")}</span>
        </div>
        {view.servers.countries.length === 0 && (
          <p className="py-4 text-[13px] text-muted-foreground">{translate("noServers")}</p>
        )}
        {view.servers.countries.map((country) => {
          const visible = !hidden.has(country.code);
          const auto = visible && !excluded.has(country.code);
          const expanded = open === country.code;
          const name = countryName(country.code);
          return (
            <div className="border-border border-t first:border-t-0" key={country.code}>
              <div className={cn("flex items-center gap-2 py-2.5", !visible && "opacity-55")}>
                <button
                  aria-expanded={expanded}
                  className="flex min-w-0 flex-1 items-center gap-2 text-left"
                  onClick={() => setOpen(expanded ? null : country.code)}
                  type="button"
                >
                  <span aria-hidden className="text-[17px]">
                    {flag(country.code)}
                  </span>
                  <span className="flex min-w-0 flex-col">
                    <span className="truncate font-medium text-[14px]">{name}</span>
                    <span className="text-[12px] text-subtle-foreground">
                      {translate("serverCount", { count: country.servers.length })}
                      {(measuring || best(country.servers) !== undefined) && (
                        <>
                          {" · "}
                          <Ping inline measuring={measuring} value={best(country.servers)} />
                        </>
                      )}
                    </span>
                    <ServiceChips servers={country.servers} />
                  </span>
                  <ChevronDown
                    aria-hidden
                    className={cn(
                      "size-4 shrink-0 text-subtle-foreground transition-transform",
                      expanded && "rotate-180",
                    )}
                  />
                </button>
                <span className="flex w-[52px] justify-center">
                  <Switch
                    aria-label={translate("visibleLabel", { country: name })}
                    checked={visible}
                    onCheckedChange={(on) =>
                      onChange({
                        ...draft,
                        hiddenCountries: toggle(draft.hiddenCountries, country.code, !on),
                        autoExcluded: on
                          ? draft.autoExcluded
                          : toggle(draft.autoExcluded, country.code, false),
                      })
                    }
                  />
                </span>
                <span className="flex w-[52px] justify-center">
                  <Switch
                    aria-label={translate("autoLabel", { country: name })}
                    checked={auto}
                    onCheckedChange={(on) =>
                      onChange({
                        ...draft,
                        autoExcluded: toggle(draft.autoExcluded, country.code, !on),
                        hiddenCountries: on
                          ? toggle(draft.hiddenCountries, country.code, false)
                          : draft.hiddenCountries,
                      })
                    }
                  />
                </span>
              </div>
              {expanded && (
                <ul className="space-y-1.5 pb-3 pl-7">
                  {country.servers.map((server) => (
                    <li className="flex items-center gap-2 text-[13px]" key={key(server)}>
                      <span className="flex min-w-0 flex-1 flex-col">
                        <span className="truncate text-muted-foreground">{server.name}</span>
                        {country.servers.length > 1 && <ServiceChips servers={[server]} />}
                      </span>
                      {server.down ? (
                        <Badge variant="danger">{translate("down")}</Badge>
                      ) : (
                        <Ping measuring={measuring} value={pings[key(server)]} />
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          );
        })}
      </section>

      {view.servers.reserve.length > 0 && (
        <section className="space-y-2 rounded-lg border border-border bg-card p-4">
          <div className="flex items-center gap-2">
            <ShieldHalf aria-hidden className="size-4" />
            <span className="flex-1 font-medium text-[14px]">{translate("reserveTitle")}</span>
            <Switch
              aria-label={translate("reserveTitle")}
              checked={draft.reserve}
              onCheckedChange={(on) => onChange({ ...draft, reserve: on })}
            />
          </div>
          <p className="text-[12.5px] text-muted-foreground">
            {translate("reserveDescription", { count: view.servers.reserve.length })}
          </p>
          <ul className="space-y-1.5 pt-1">
            {view.servers.reserve.map((server) => (
              <li className="flex items-center gap-2 text-[13px]" key={key(server)}>
                <span className="flex min-w-0 flex-1 flex-col">
                  <span className="truncate text-muted-foreground">{server.name}</span>
                  <ServiceChips servers={[server]} />
                </span>
                {server.down ? (
                  <Badge variant="danger">{translate("down")}</Badge>
                ) : (
                  <Ping measuring={measuring} value={pings[key(server)]} />
                )}
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}

function RoutesTab({
  draft,
  onChange,
  view,
}: {
  draft: Preferences;
  onChange: (next: Preferences) => void;
  view: SmartView;
}) {
  const translate = useTranslations("account.smart");
  const [domain, setDomain] = useState("");
  const [action, setAction] = useState<Action>("direct");
  const [problem, setProblem] = useState("");

  const presets: (keyof Presets)[] = ["russiaDirect", "blockAds", "blockQuic"];

  function add(event: FormEvent) {
    event.preventDefault();
    let value = domain.trim().toLowerCase();
    value =
      value
        .replace(/^[a-z]+:\/\//, "")
        .split(/[/?#]/)[0]
        ?.replace(/^\*\./, "")
        .replace(/:\d+$/, "")
        .replace(/^www\.(?=.+\.)/, "") ?? "";
    if (!/^([\p{L}\p{N}-]+\.)+[\p{L}\p{N}-]{2,}$/u.test(value)) {
      setProblem(translate("domainInvalid"));
      return;
    }
    if (draft.rules.some((rule) => rule.domain === value)) {
      setProblem(translate("domainDuplicate"));
      return;
    }
    if (draft.rules.length >= view.maxRules) {
      setProblem(translate("rulesFull", { max: view.maxRules }));
      return;
    }
    onChange({ ...draft, rules: [{ action, domain: value }, ...draft.rules] });
    setDomain("");
  }

  return (
    <div className="space-y-4">
      <section className="rounded-lg border border-border bg-card px-4">
        {presets.map((preset) => (
          <label
            className="flex items-center gap-3 border-border border-t py-3 first:border-t-0"
            key={preset}
          >
            <span className="min-w-0 flex-1">
              <span className="block font-medium text-[14px]">
                {translate(`presets.${preset}.title`)}
              </span>
              <span className="block text-[12.5px] text-muted-foreground">
                {translate(`presets.${preset}.description`)}
              </span>
            </span>
            <Switch
              checked={draft.presets[preset]}
              onCheckedChange={(on) =>
                onChange({ ...draft, presets: { ...draft.presets, [preset]: on } })
              }
            />
          </label>
        ))}
      </section>

      {view.customRules && (
        <section className="space-y-3 rounded-lg border border-border bg-card p-4">
          <div className="flex items-baseline justify-between">
            <span className="font-medium text-[14px]">{translate("rulesTitle")}</span>
            <span className="text-[12.5px] text-subtle-foreground">
              {translate("rulesCount", { count: draft.rules.length, max: view.maxRules })}
            </span>
          </div>
          <form className="flex gap-2" noValidate onSubmit={add}>
            <Input
              aria-invalid={problem !== ""}
              aria-label={translate("domainLabel")}
              className="min-w-0 flex-1"
              inputMode="url"
              onChange={(event) => {
                setDomain(event.target.value);
                setProblem("");
              }}
              placeholder="example.com"
              value={domain}
            />
            <Select onValueChange={(value) => setAction(value as Action)} value={action}>
              <SelectTrigger aria-label={translate("actionLabel")} className="w-[130px]">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ACTIONS.map((option) => (
                  <SelectItem key={option} value={option}>
                    {translate(`actions.${option}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button aria-label={translate("addRule")} size="icon" type="submit">
              <Plus aria-hidden />
            </Button>
          </form>
          {problem && (
            <p className="text-[12.5px] text-destructive" role="alert">
              {problem}
            </p>
          )}
          {draft.rules.length === 0 ? (
            <p className="text-[12.5px] text-muted-foreground">{translate("rulesEmpty")}</p>
          ) : (
            <ul>
              {draft.rules.map((rule) => (
                <li
                  className="flex items-center gap-2 border-border border-t py-2 first:border-t-0"
                  key={rule.domain}
                >
                  <span className="min-w-0 flex-1 truncate font-mono text-[12.5px]">
                    {rule.domain}
                  </span>
                  <Badge
                    variant={
                      rule.action === "block"
                        ? "danger"
                        : rule.action === "proxy"
                          ? "info"
                          : "neutral"
                    }
                  >
                    {translate(`actions.${rule.action}`)}
                  </Badge>
                  <Button
                    aria-label={translate("removeRule", { domain: rule.domain })}
                    onClick={() =>
                      onChange({
                        ...draft,
                        rules: draft.rules.filter((r) => r.domain !== rule.domain),
                      })
                    }
                    size="icon-sm"
                    variant="ghost"
                  >
                    <X aria-hidden />
                  </Button>
                </li>
              ))}
            </ul>
          )}
          <p className="text-[12px] text-muted-foreground">{translate("rulesHint")}</p>
        </section>
      )}
    </div>
  );
}
