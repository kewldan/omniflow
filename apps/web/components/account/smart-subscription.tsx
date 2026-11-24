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
  ChevronDown,
  ChevronRight,
  CircleCheck,
  CircleHelp,
  Gauge,
  Globe,
  House,
  Lock,
  Magnet,
  Megaphone,
  Minus,
  Pin,
  Plus,
  RotateCw,
  Route,
  ShieldHalf,
  Sparkles,
  X,
  Zap,
} from "lucide-react";
import Link from "next/link";
import { useLocale, useTranslations } from "next-intl";
import { type FormEvent, useEffect, useMemo, useRef, useState } from "react";
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
import { estimatePing, type PingModel } from "@/lib/ping-estimate";
import { ServiceLogo } from "@/lib/service-logos";

type Action = "direct" | "proxy" | "block";
type Rule = { domain: string; action: Action };
type RuleKind = "domains" | "russia" | "ai" | "torrents" | "ads" | "quic";
type RuleMode = "locked" | "on" | "off" | "disabled";
/** One of the operator's service rules: a card on the Routing tab. */
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
type Preferences = {
  hiddenCountries: string[];
  autoExcluded: string[];
  reserve: boolean;
  /** Choices on the rules the customer may change; a rule left out follows its default. */
  toggles: Record<string, boolean>;
  rules: Rule[];
  /** The host the customer made their own server; empty for none. */
  pinned: string;
  whitelistMode: boolean;
};
type Server = {
  id: string;
  name: string;
  address: string;
  port: number;
  down: boolean;
  /** What the latest geocheck of the server's exit node found. */
  services?: NodeServices;
  /** Turns the browser's timing into an estimate through the tunnel. */
  ping?: PingModel;
};
type SmartView = {
  servers: { countries: { code: string; servers: Server[] }[]; reserve: Server[] };
  preferences: Preferences;
  customized: boolean;
  customRules: boolean;
  maxRules: number;
  /** The service's rules in the operator's order; disabled ones are left out. */
  rules: ServiceRule[];
  /** For each AI service, how many of the customer's servers it answers from. */
  aiServers: Record<string, number>;
  /** Whether the operator offers the whitelist mode. */
  whitelistMode: boolean;
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
  estimated = false,
  inline = false,
  measuring,
  value,
}: {
  /** The value is an estimate through the tunnel, not the raw timing. */
  estimated?: boolean;
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
  // An estimate is one round trip; the raw timing is about two.
  const [good, fair] = estimated ? [80, 160] : [120, 250];
  const tone = value < good ? "text-success" : value < fair ? "text-warning" : "text-destructive";
  return (
    <span
      className={cn(shape, tone)}
      title={estimated ? translate("pingEstimateTitle") : undefined}
    >
      {translate(estimated ? "msEstimated" : "ms", { value })}
    </span>
  );
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
  // After a save the screen says what to do next: the app shows the change
  // only once it refreshes the subscription.
  const [justSaved, setJustSaved] = useState(false);
  const [intro, setIntro] = useIntro();

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
      setJustSaved(true);
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
      setJustSaved(true);
      toast.success(translate("resetDone"));
    } catch (resetError) {
      toast.error(resetError instanceof ApiError ? resetError.message : shared("states.error"));
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="animate-step-in space-y-4 pb-4">
      <div className="flex items-center justify-between gap-3">
        <h1 className="font-semibold text-[19px] tracking-[-0.02em]">{translate("title")}</h1>
        {!intro && (
          <Button onClick={() => setIntro(true)} size="sm" variant="ghost">
            <CircleHelp aria-hidden />
            {translate("introOpen")}
          </Button>
        )}
      </div>
      {intro && <IntroCard onClose={() => setIntro(false)} />}
      {justSaved && !dirty && <SavedCard onClose={() => setJustSaved(false)} />}
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

      {/* With unsaved changes the buttons float above the tab bar, so a
          customer who changed something at the top of a long list does not
          have to find the end of the page to keep it. */}
      <div
        className={cn(
          "space-y-2",
          dirty &&
            "sticky bottom-[88px] z-20 rounded-xl border border-border bg-card/95 p-3 shadow-lg backdrop-blur md:bottom-4",
        )}
      >
        {dirty && <p className="px-1 font-medium text-[13px]">{translate("unsaved")}</p>}
        <div className="flex gap-2">
          <Button
            className="flex-1"
            disabled={!dirty || saving}
            onClick={() => void save()}
            size="lg"
          >
            {shared("actions.save")}
          </Button>
          {dirty ? (
            <Button
              disabled={saving}
              onClick={() => setDraft(structuredClone(data.preferences))}
              size="lg"
              variant="outline"
            >
              {translate("discard")}
            </Button>
          ) : (
            data.customized && (
              <Button disabled={saving} onClick={() => void reset()} size="lg" variant="outline">
                {translate("reset")}
              </Button>
            )
          )}
        </div>
        {!dirty && (
          <p className="px-1 text-[12.5px] text-muted-foreground">
            {translate("appliesOnRefresh")}
          </p>
        )}
      </div>
    </div>
  );
}

const INTRO_KEY = "omniflow.smart.intro.closed";

/** Whether the how-it-works card shows: until the customer closes it once. */
function useIntro(): [boolean, (open: boolean) => void] {
  const [open, setOpen] = useState(false);
  useEffect(() => {
    try {
      setOpen(window.localStorage.getItem(INTRO_KEY) !== "1");
    } catch {
      setOpen(true);
    }
  }, []);
  return [
    open,
    (next: boolean) => {
      setOpen(next);
      try {
        if (next) {
          window.localStorage.removeItem(INTRO_KEY);
        } else {
          window.localStorage.setItem(INTRO_KEY, "1");
        }
      } catch {
        // Private mode: the card simply shows again next time.
      }
    },
  ];
}

/**
 * What this screen is for, in three steps, and the reassurance that nothing
 * has to be touched: the defaults already work.
 */
function IntroCard({ onClose }: { onClose: () => void }) {
  const translate = useTranslations("account.smart.intro");
  const steps = [
    { icon: Globe, key: "servers" },
    { icon: Route, key: "routes" },
    { icon: RotateCw, key: "refresh" },
  ] as const;
  return (
    <section className="space-y-3 rounded-lg border border-border bg-card p-4">
      <div className="flex items-start gap-2">
        <div className="min-w-0 flex-1">
          <h2 className="font-medium text-[14.5px]">{translate("title")}</h2>
          <p className="text-[12.5px] text-muted-foreground">{translate("lead")}</p>
        </div>
        <Button aria-label={translate("close")} onClick={onClose} size="icon-sm" variant="ghost">
          <X aria-hidden />
        </Button>
      </div>
      <ol className="space-y-2.5">
        {steps.map(({ icon: Icon, key }, index) => (
          <li className="flex gap-3" key={key}>
            <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-secondary">
              <Icon aria-hidden className="size-4" />
            </span>
            <span className="min-w-0">
              <span className="block font-medium text-[13.5px]">
                {index + 1}. {translate(`${key}.title`)}
              </span>
              <span className="block text-[12.5px] text-muted-foreground">
                {translate(`${key}.text`)}
              </span>
            </span>
          </li>
        ))}
      </ol>
      <p className="rounded-md bg-secondary px-3 py-2 text-[12.5px]">{translate("defaults")}</p>
      <Button className="w-full" onClick={onClose} variant="outline">
        {translate("gotIt")}
      </Button>
    </section>
  );
}

/** After a save: the change reaches the app when it refreshes, and how to. */
function SavedCard({ onClose }: { onClose: () => void }) {
  const translate = useTranslations("account.smart.savedCard");
  return (
    <section
      aria-live="polite"
      className="space-y-2 rounded-lg border border-success/30 bg-success/10 p-4"
      role="status"
    >
      <div className="flex items-start gap-2">
        <CircleCheck aria-hidden className="mt-0.5 size-4 shrink-0 text-success" />
        <div className="min-w-0 flex-1">
          <h2 className="font-medium text-[14px]">{translate("title")}</h2>
          <p className="text-[12.5px] text-muted-foreground">{translate("text")}</p>
        </div>
        <Button aria-label={translate("close")} onClick={onClose} size="icon-sm" variant="ghost">
          <X aria-hidden />
        </Button>
      </div>
      <ul className="space-y-1 pl-6 text-[12.5px]">
        <li>{translate("happ")}</li>
        <li>{translate("incy")}</li>
        <li>{translate("other")}</li>
      </ul>
    </section>
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
  const pinned = findPinned(view, draft);

  function pin(server: Server) {
    onChange({ ...draft, pinned: draft.pinned === server.id ? "" : server.id });
  }

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

  // The ping is measured as soon as the screen opens: nobody comes here to
  // press a button first. Once per mount, so a re-render does not restart it.
  const measuredOnOpen = useRef(false);
  // biome-ignore lint/correctness/useExhaustiveDependencies: runs once when the screen opens.
  useEffect(() => {
    if (measuredOnOpen.current) {
      return;
    }
    measuredOnOpen.current = true;
    void measureAll();
  }, []);

  function shown(server: Server) {
    return estimatePing(pings[key(server)], server.ping);
  }

  function best(servers: Server[]): { value: number | null | undefined; estimated: boolean } {
    const shownValues = servers.map(shown);
    const value = bestValue(shownValues.map((entry) => entry.value));
    return { estimated: shownValues.some((entry) => entry.estimated), value };
  }

  function bestValue(values: (number | null | undefined)[]): number | null | undefined {
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
          <RotateCw aria-hidden className={cn(measuring && "animate-spin")} />
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

      <PinnedCard
        countryName={countryName}
        onUnpin={() => onChange({ ...draft, pinned: "" })}
        pinned={pinned}
      />

      {checked && (
        <p className="px-1 text-[12.5px] text-muted-foreground">{translate("servicesHint")}</p>
      )}

      <section className="rounded-lg border border-border bg-card px-4">
        <div className="flex items-center gap-2 pt-3 pb-1 text-[11.5px] text-subtle-foreground">
          <span className="flex-1">{translate("countries")}</span>
          <span className="w-[52px] text-center">{translate("visible")}</span>
          <span className="w-[52px] text-center">{translate("auto")}</span>
        </div>
        <p className="pb-2 text-[12px] text-muted-foreground">{translate("columnsHint")}</p>
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
                      {(measuring || best(country.servers).value !== undefined) && (
                        <>
                          {" · "}
                          <Ping inline measuring={measuring} {...best(country.servers)} />
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
                        <Ping measuring={measuring} {...shown(server)} />
                      )}
                      {visible && server.id && (
                        <PinButton
                          name={server.name}
                          onClick={() => pin(server)}
                          pinned={draft.pinned === server.id}
                        />
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
              checked={draft.reserve || (view.whitelistMode && draft.whitelistMode)}
              disabled={view.whitelistMode && draft.whitelistMode}
              onCheckedChange={(on) => onChange({ ...draft, reserve: on })}
            />
          </div>
          <p className="text-[12.5px] text-muted-foreground">
            {translate("reserveDescription", { count: view.servers.reserve.length })}
          </p>
          {view.whitelistMode && (
            <label className="flex items-start gap-3 rounded-md bg-secondary p-3">
              <span className="min-w-0 flex-1">
                <span className="block font-medium text-[13.5px]">
                  {translate("whitelistModeTitle")}
                </span>
                <span className="block text-[12.5px] text-muted-foreground">
                  {translate("whitelistModeDescription")}
                </span>
              </span>
              <Switch
                checked={draft.whitelistMode}
                onCheckedChange={(on) =>
                  onChange({ ...draft, whitelistMode: on, reserve: on ? true : draft.reserve })
                }
              />
            </label>
          )}
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
                  <Ping measuring={measuring} {...shown(server)} />
                )}
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}

/** The customer's own server, if it is still among their servers. */
function findPinned(view: SmartView, draft: Preferences): { code: string; server: Server } | null {
  if (!draft.pinned) {
    return null;
  }
  for (const country of view.servers.countries) {
    const server = country.servers.find((candidate) => candidate.id === draft.pinned);
    if (server) {
      return { code: country.code, server };
    }
  }
  return null;
}

/**
 * «My server»: one exit address the app opens first. It says why someone
 * would want that — sites see the same IP and ask for captchas and new
 * sign-ins less often — and how to choose one.
 */
function PinnedCard({
  countryName,
  onUnpin,
  pinned,
}: {
  countryName: (code: string) => string;
  onUnpin: () => void;
  pinned: { code: string; server: Server } | null;
}) {
  const translate = useTranslations("account.smart");
  return (
    <section className="space-y-2 rounded-lg border border-border bg-card p-4">
      <div className="flex items-center gap-2">
        <Pin aria-hidden className="size-4" />
        <span className="flex-1 font-medium text-[14px]">{translate("pinnedTitle")}</span>
        {pinned && (
          <Button onClick={onUnpin} size="sm" variant="ghost">
            {translate("unpin")}
          </Button>
        )}
      </div>
      {pinned ? (
        <p className="flex items-center gap-2 text-[13.5px]">
          <span aria-hidden className="text-[17px]">
            {flag(pinned.code)}
          </span>
          <span className="min-w-0 flex-1 truncate">
            {countryName(pinned.code)} · {pinned.server.name}
          </span>
        </p>
      ) : (
        <p className="text-[12.5px] text-muted-foreground">{translate("pinnedHowTo")}</p>
      )}
      <p className="text-[12.5px] text-muted-foreground">{translate("pinnedWhy")}</p>
    </section>
  );
}

function PinButton({
  name,
  onClick,
  pinned,
}: {
  name: string;
  onClick: () => void;
  pinned: boolean;
}) {
  const translate = useTranslations("account.smart");
  return (
    <Button
      aria-label={translate(pinned ? "unpinServer" : "pinServer", { server: name })}
      aria-pressed={pinned}
      className={cn(!pinned && "text-subtle-foreground")}
      onClick={onClick}
      size="icon-sm"
      title={translate(pinned ? "unpinServer" : "pinServer", { server: name })}
      variant={pinned ? "secondary" : "ghost"}
    >
      {pinned ? <Pin aria-hidden className="fill-current" /> : <Pin aria-hidden />}
    </Button>
  );
}

const GROUPS: Action[] = ["direct", "proxy", "block"];
const BUILTIN_ICONS: Record<Exclude<RuleKind, "domains">, typeof Globe> = {
  ads: Megaphone,
  ai: Sparkles,
  quic: Gauge,
  russia: House,
  torrents: Magnet,
};
const AI_NAMES: Record<string, string> = { chatgpt: "ChatGPT", claude: "Claude", gemini: "Gemini" };

/** Whether a rule applies to this customer. */
function applies(rule: ServiceRule, toggles: Record<string, boolean>): boolean {
  if (rule.mode === "locked") {
    return true;
  }
  return toggles[rule.id] ?? rule.mode === "on";
}

function RuleIcon({ rule }: { rule: ServiceRule }) {
  if (rule.icon) {
    return (
      // biome-ignore lint/performance/noImgElement: a small icon the API serves; next/image adds nothing here.
      <img
        alt=""
        className="size-9 shrink-0 rounded-md bg-white object-contain p-1"
        height={36}
        loading="lazy"
        src={`/v1/account/smart-subscription/icons/${encodeURIComponent(rule.icon)}`}
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

/** The rule's caption: the operator's, else the panel's words for its kind. */
function useRuleText() {
  const translate = useTranslations("account.smart.builtin");
  return (rule: ServiceRule) => {
    if (rule.kind === "domains") {
      return { caption: rule.caption, name: rule.name };
    }
    const kind = rule.kind === "torrents" ? `torrents_${rule.action}` : rule.kind;
    return {
      caption: rule.caption || translate(`${kind}.caption`),
      name: rule.name || translate(`${kind}.name`),
    };
  };
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
  function setToggle(rule: ServiceRule, on: boolean) {
    const toggles = { ...draft.toggles };
    // A choice equal to the default is not a choice: the saved document and
    // the draft stay comparable.
    if (on === (rule.mode === "on")) {
      delete toggles[rule.id];
    } else {
      toggles[rule.id] = on;
    }
    onChange({ ...draft, toggles });
  }

  return (
    <div className="space-y-5">
      {GROUPS.map((action) => {
        const rules = view.rules.filter((rule) => rule.action === action);
        if (rules.length === 0) {
          return null;
        }
        return (
          <section className="space-y-2" key={action}>
            <div className="px-1">
              <h2 className="font-medium text-[14px]">{translate(`groups.${action}.title`)}</h2>
              <p className="text-[12.5px] text-muted-foreground">
                {translate(`groups.${action}.description`)}
              </p>
            </div>
            <ul className="rounded-lg border border-border bg-card px-4">
              {rules.map((rule) => (
                <RuleCard
                  aiServers={view.aiServers}
                  key={rule.id}
                  on={applies(rule, draft.toggles)}
                  onToggle={(on) => setToggle(rule, on)}
                  rule={rule}
                />
              ))}
            </ul>
          </section>
        );
      })}

      {view.customRules && <CustomRules draft={draft} max={view.maxRules} onChange={onChange} />}
    </div>
  );
}

function RuleCard({
  aiServers,
  on,
  onToggle,
  rule,
}: {
  aiServers: Record<string, number>;
  on: boolean;
  onToggle: (on: boolean) => void;
  rule: ServiceRule;
}) {
  const translate = useTranslations("account.smart");
  const text = useRuleText();
  const [open, setOpen] = useState(false);
  const { caption, name } = text(rule);
  const locked = rule.mode === "locked";
  const ai = rule.kind === "ai" ? Object.entries(aiServers).filter(([, count]) => count > 0) : [];
  return (
    <li className="border-border border-t py-3 first:border-t-0">
      <div className="flex items-start gap-3">
        <RuleIcon rule={rule} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            <span className="truncate font-medium text-[14px]">{name}</span>
            {locked && <Lock aria-hidden className="size-3.5 shrink-0 text-subtle-foreground" />}
          </div>
          {caption && (
            <p className="mt-0.5 whitespace-pre-line text-[12.5px] text-muted-foreground">
              {caption}
            </p>
          )}
          {rule.kind === "ai" && (
            <p className="mt-1 flex flex-wrap gap-1">
              {ai.length === 0 ? (
                <span className="text-[12px] text-subtle-foreground">{translate("aiNone")}</span>
              ) : (
                ai.map(([service, count]) => (
                  <Badge className="px-1.5 font-normal" key={service} variant="success">
                    {service in NODE_SERVICE_LABELS && (
                      <ServiceLogo className="size-3" service={service as NodeService} />
                    )}
                    {translate("aiServers", { count, service: AI_NAMES[service] ?? service })}
                  </Badge>
                ))
              )}
            </p>
          )}
          {rule.kind === "domains" && rule.domains.length > 0 && (
            <button
              aria-expanded={open}
              className="mt-1 text-[12px] text-subtle-foreground underline-offset-2 hover:underline"
              onClick={() => setOpen(!open)}
              type="button"
            >
              {translate("ruleDomains", { count: rule.domains.length })}
            </button>
          )}
          {open && (
            <p className="mt-1 break-words font-mono text-[11.5px] text-muted-foreground">
              {rule.domains.join(", ")}
            </p>
          )}
        </div>
        <div className="flex shrink-0 flex-col items-end gap-1 pt-0.5">
          <Switch aria-label={name} checked={on} disabled={locked} onCheckedChange={onToggle} />
          {locked && (
            <span className="text-[11px] text-subtle-foreground">{translate("ruleAlways")}</span>
          )}
          {rule.kind === "torrents" && !locked && !on && (
            <span className="text-[11px] text-destructive">{translate("torrentsOff")}</span>
          )}
        </div>
      </div>
    </li>
  );
}

function CustomRules({
  draft,
  max,
  onChange,
}: {
  draft: Preferences;
  max: number;
  onChange: (next: Preferences) => void;
}) {
  const translate = useTranslations("account.smart");
  const [domain, setDomain] = useState("");
  const [action, setAction] = useState<Action>("direct");
  const [problem, setProblem] = useState("");

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
    if (draft.rules.length >= max) {
      setProblem(translate("rulesFull", { max }));
      return;
    }
    onChange({ ...draft, rules: [{ action, domain: value }, ...draft.rules] });
    setDomain("");
  }

  return (
    <section className="space-y-3 rounded-lg border border-border bg-card p-4">
      <div className="flex items-baseline justify-between">
        <span className="font-medium text-[14px]">{translate("rulesTitle")}</span>
        <span className="text-[12.5px] text-subtle-foreground">
          {translate("rulesCount", { count: draft.rules.length, max })}
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
              <span className="min-w-0 flex-1 truncate font-mono text-[12.5px]">{rule.domain}</span>
              <Badge
                variant={
                  rule.action === "block" ? "danger" : rule.action === "proxy" ? "info" : "neutral"
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
  );
}
