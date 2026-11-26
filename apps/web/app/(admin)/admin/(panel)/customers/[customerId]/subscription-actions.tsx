"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { DateTimeField } from "@omniflow/ui/date-time-field";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@omniflow/ui/tabs";
import {
  CalendarClock,
  Clock,
  Gauge,
  Pause,
  Play,
  Power,
  PowerOff,
  RotateCcw,
  Server,
} from "lucide-react";
import { useTranslations } from "next-intl";
import { type ReactNode, useId, useState } from "react";
import useSWR from "swr";
import { ReasonDialog } from "@/components/admin/reason-dialog";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";
import { type Listing, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";
import { SquadPicker } from "../../catalog/squad-picker";

type PanelDevice = {
  ref: string;
  platform?: string;
  model?: string;
  osVersion?: string;
  lastSeenAt: string;
};

/**
 * The ready reasons for each subscription action, under admin.reasons.<group>.
 * Each is phrased for whoever reads the audit log or the customer's history.
 */
const PRESETS = {
  subscriptionEnable: ["paidOutside", "disabledByMistake", "reviewCleared", "appealAccepted"],
  subscriptionDisable: ["refunded", "sharing", "abuse", "customerAsked"],
  resetTraffic: ["downtime", "clientBug", "paidOutside", "supportApproved"],
  subscriptionPause: ["customerAway", "paymentDispute", "review"],
  subscriptionResume: ["customerBack", "disputeResolved", "reviewCleared"],
  subscriptionExtend: ["downtime", "paidOutside", "notApplied", "giveaway", "wrongExpiry"],
  subscriptionLimits: ["paidOutside", "wrongLimits", "goodwillTraffic", "sharingLimit"],
  subscriptionSquads: ["blockedServer", "wrongSquads", "locationApproved", "testing"],
  squadAccessGrant: ["blockedWorkaround", "tryLocation", "troubleshooting"],
  squadAccessRevoke: ["noLongerNeeded", "mistake", "misuse"],
  deviceRemove: ["replaced", "freeSlot", "unknownDevice", "stale"],
} as const;
type PresetGroup = keyof typeof PRESETS;

/** The translated ready reasons of one action. */
function usePresets() {
  const translate = useTranslations("admin.reasons");
  return (group: PresetGroup) => PRESETS[group].map((key) => translate(`${group}.${key}`));
}

type Run = ReturnType<typeof useOperatorAction>["run"];

/** An action waiting for its reason: what it is called, and what runs once one is given. */
type PendingAction = {
  title: string;
  description?: string;
  presets: string[];
  destructive?: boolean;
  perform: (reason: string) => Promise<boolean>;
};

/** The subscription as it stands, for the defaults and the "+N days" buttons. */
type CurrentState = {
  endsAt?: string;
  trafficAllowanceBytes?: number;
  deviceLimit?: number;
};

const GIB = 1024 ** 3;
const EXTEND_STEPS = [7, 30, 90, 365];

/**
 * Lifecycle actions for one subscription.
 *
 * One-tap actions sit in a row with their icons; changes that take a value are
 * grouped by what they change. Every action asks for its reason in a dialog at
 * the moment it is taken — a ready reason for that action, or the operator's
 * own — rather than a reason field that greys out the whole panel until
 * something is typed into it.
 *
 * Every action goes through the fulfillment pipeline rather than writing to
 * Remnawave directly, so an operator change carries the same idempotency key,
 * retry policy, drift detection, and history as one a purchase produced. That
 * is why the panel reports "queued" rather than "done": the worker applies it.
 */
export function SubscriptionActions({
  current = {},
  customerId,
  subscriptionId,
  hasEntitlement,
}: {
  current?: CurrentState;
  customerId: string;
  subscriptionId: string;
  hasEntitlement: boolean;
}) {
  const translate = useTranslations("admin.customers");
  const presets = usePresets();
  const { can } = useSession();
  const endsAtId = useId();
  const trafficId = useId();
  const deviceLimitId = useId();
  const squadsId = useId();

  const [endsAt, setEndsAt] = useState("");
  const [trafficGb, setTrafficGb] = useState("");
  const [deviceLimit, setDeviceLimit] = useState("");
  const [squads, setSquads] = useState<string[]>([]);
  const [queued, setQueued] = useState("");
  const [asking, setAsking] = useState<PendingAction | null>(null);
  const { run, pending, error } = useOperatorAction();

  if (!can("subscriptions.write")) {
    return null;
  }

  const base = `/v1/panel/customers/${customerId}/subscriptions/${subscriptionId}`;

  /** Queues a fulfillment operation once its reason is given. */
  function operation(
    title: string,
    name: string,
    group: PresetGroup,
    body: Record<string, unknown> = {},
  ) {
    setAsking({
      destructive: name === "disable",
      perform: async (reason) => {
        const ok = await run(`${base}/operations`, {
          body: { operation: name, ...body },
          method: "POST",
          reason,
        });
        if (ok) {
          setQueued(title);
        }
        return ok;
      },
      presets: presets(group),
      title,
    });
  }

  /**
   * Pause and resume have their own routes rather than being two more values
   * on the operations endpoint: they change Remnawave *and* the entitlement's
   * own clock, and the two commit together.
   */
  function transition(kind: "pause" | "resume") {
    const title = translate(`pause.${kind}`);
    setAsking({
      description: translate(kind === "pause" ? "pause.pauseWarning" : "pause.resumeWarning"),
      destructive: kind === "pause",
      perform: async (reason) => {
        const ok = await run(`${base}/${kind}`, { method: "POST", reason });
        if (ok) {
          setQueued(title);
        }
        return ok;
      },
      presets: presets(kind === "pause" ? "subscriptionPause" : "subscriptionResume"),
      title,
    });
  }

  // "+N days" counts from the current expiry, or from now for one that has
  // already run out, which is what extending a lapsed subscription means.
  const extendFrom = Math.max(Date.now(), current.endsAt ? Date.parse(current.endsAt) : 0);
  const disabled = !hasEntitlement || pending;

  return (
    <div className="mt-3 flex flex-col gap-3 rounded-lg border bg-well border-border p-3">
      {!hasEntitlement && (
        // A slot that has never been provisioned has nothing to change.
        <p className="text-muted-foreground text-xs">{translate("actions.notProvisioned")}</p>
      )}

      <div className="flex flex-wrap gap-2">
        <QuickAction
          disabled={disabled}
          icon={<Power className="size-4" />}
          label={translate("actions.enable")}
          onClick={() => operation(translate("actions.enable"), "enable", "subscriptionEnable")}
        />
        <QuickAction
          disabled={disabled}
          icon={<PowerOff className="size-4" />}
          label={translate("actions.disable")}
          onClick={() => operation(translate("actions.disable"), "disable", "subscriptionDisable")}
        />
        <QuickAction
          disabled={disabled}
          icon={<RotateCcw className="size-4" />}
          label={translate("actions.resetTraffic")}
          onClick={() =>
            operation(translate("actions.resetTraffic"), "reset_traffic", "resetTraffic")
          }
        />
        {/* Pausing suspends access without spending the remaining days, which
            is what "disable" does not do. Both are offered and the API refuses
            the one that does not apply. */}
        <QuickAction
          disabled={disabled}
          icon={<Pause className="size-4" />}
          label={translate("pause.pause")}
          onClick={() => transition("pause")}
        />
        <QuickAction
          disabled={disabled}
          icon={<Play className="size-4" />}
          label={translate("pause.resume")}
          onClick={() => transition("resume")}
        />
      </div>

      <Tabs defaultValue="term">
        <TabsList className="flex-wrap">
          <TabsTrigger className="gap-1.5" value="term">
            <CalendarClock aria-hidden className="size-3.5" />
            {translate("actions.tabs.term")}
          </TabsTrigger>
          <TabsTrigger className="gap-1.5" value="limits">
            <Gauge aria-hidden className="size-3.5" />
            {translate("actions.tabs.limits")}
          </TabsTrigger>
          <TabsTrigger className="gap-1.5" value="servers">
            <Server aria-hidden className="size-3.5" />
            {translate("actions.tabs.servers")}
          </TabsTrigger>
          <TabsTrigger className="gap-1.5" value="access">
            <Clock aria-hidden className="size-3.5" />
            {translate("actions.tabs.access")}
          </TabsTrigger>
        </TabsList>

        <TabsContent className="flex flex-col gap-3 pt-3" value="term">
          {current.endsAt && (
            <p className="text-muted-foreground text-xs">
              {translate("actions.currentEnd")} <Timestamp value={current.endsAt} />
            </p>
          )}
          <div className="flex flex-wrap gap-2">
            {EXTEND_STEPS.map((days) => (
              <Button
                disabled={disabled}
                key={days}
                onClick={() =>
                  operation(
                    translate("actions.extendBy", { days }),
                    "extend",
                    "subscriptionExtend",
                    {
                      endsAt: new Date(extendFrom + days * 86_400_000).toISOString(),
                    },
                  )
                }
                size="sm"
                variant="secondary"
              >
                {translate("actions.extendBy", { days })}
              </Button>
            ))}
          </div>
          <div className="flex flex-wrap items-end gap-2">
            <div className="flex min-w-56 flex-col gap-1.5">
              <Label htmlFor={endsAtId}>{translate("actions.endsAt")}</Label>
              <DateTimeField
                hourLabel={translate("actions.hour")}
                id={endsAtId}
                minuteLabel={translate("actions.minute")}
                onChange={setEndsAt}
                placeholder={translate("actions.pickMoment")}
                value={endsAt}
              />
            </div>
            <Button
              disabled={disabled || endsAt === ""}
              onClick={() =>
                operation(translate("actions.extend"), "extend", "subscriptionExtend", {
                  endsAt: new Date(endsAt).toISOString(),
                })
              }
              size="sm"
            >
              {translate("actions.extend")}
            </Button>
          </div>
        </TabsContent>

        <TabsContent className="flex flex-col gap-3 pt-3" value="limits">
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={trafficId}>{translate("actions.trafficGb")}</Label>
              <div className="relative">
                <Input
                  className="pr-10"
                  id={trafficId}
                  inputMode="decimal"
                  onChange={(event) => setTrafficGb(event.target.value)}
                  placeholder={
                    current.trafficAllowanceBytes
                      ? String(Math.round(current.trafficAllowanceBytes / GIB))
                      : translate("subscription.unlimited")
                  }
                  value={trafficGb}
                />
                <span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-muted-foreground text-xs">
                  {translate("actions.gb")}
                </span>
              </div>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={deviceLimitId}>{translate("actions.deviceLimit")}</Label>
              <Input
                id={deviceLimitId}
                inputMode="numeric"
                onChange={(event) => setDeviceLimit(event.target.value)}
                placeholder={current.deviceLimit ? String(current.deviceLimit) : "—"}
                value={deviceLimit}
              />
            </div>
          </div>
          <p className="text-muted-foreground text-xs">{translate("actions.limitsHint")}</p>
          <Button
            className="self-start"
            disabled={disabled || (trafficGb.trim() === "" && deviceLimit.trim() === "")}
            onClick={() =>
              operation(translate("actions.setLimits"), "set_limits", "subscriptionLimits", {
                deviceLimit: deviceLimit.trim() === "" ? null : Number(deviceLimit),
                trafficAllowanceBytes:
                  trafficGb.trim() === ""
                    ? null
                    : Math.round(Number(trafficGb.replace(",", ".")) * GIB),
              })
            }
            size="sm"
          >
            {translate("actions.setLimits")}
          </Button>
        </TabsContent>

        {/* Squads are picked by name from the panel's list; the change goes
            through the same fulfillment pipeline as every other one. */}
        <TabsContent className="flex flex-col gap-3 pt-3" value="servers">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={squadsId}>{translate("actions.squads")}</Label>
            <SquadPicker
              control={{ id: squadsId, "aria-invalid": false }}
              multiple
              onChange={setSquads}
              value={squads}
            />
          </div>
          <Button
            className="self-start"
            disabled={disabled || squads.length === 0}
            onClick={() =>
              operation(translate("actions.setSquads"), "set_squads", "subscriptionSquads", {
                squadIds: squads,
              })
            }
            size="sm"
          >
            {translate("actions.setSquads")}
          </Button>
        </TabsContent>

        <TabsContent className="pt-3" value="access">
          <TemporarySquadAccess
            ask={setAsking}
            customerId={customerId}
            disabled={disabled}
            run={run}
            subscriptionId={subscriptionId}
          />
        </TabsContent>
      </Tabs>

      {error && <p className="text-danger-foreground text-sm">{error.message}</p>}
      {queued && (
        // The worker applies it, so the honest report is "queued", not "done".
        <p className="text-muted-foreground text-sm">
          {translate("actions.queued", { operation: queued })}
        </p>
      )}

      <ReasonDialog
        confirmLabel={translate("actions.confirm")}
        description={asking?.description}
        destructive={asking?.destructive}
        error={error?.message}
        onConfirm={async (reason) => {
          if (asking && (await asking.perform(reason))) {
            setAsking(null);
          }
        }}
        onOpenChange={(open) => !open && setAsking(null)}
        open={asking !== null}
        pending={pending}
        presets={asking?.presets ?? []}
        title={asking?.title ?? ""}
      />
    </div>
  );
}

function QuickAction({
  disabled,
  icon,
  label,
  onClick,
}: {
  disabled: boolean;
  icon: ReactNode;
  label: string;
  onClick: () => void;
}) {
  return (
    <Button className="gap-1.5" disabled={disabled} onClick={onClick} size="sm" variant="outline">
      <span aria-hidden className="text-muted-foreground">
        {icon}
      </span>
      {label}
    </Button>
  );
}

type SquadGrant = {
  id: string;
  squadId: string;
  source: string;
  status: string;
  endsAt: string;
};

/**
 * Temporary access to one squad for some hours, on top of what the
 * subscription holds. The grant goes through fulfillment like every other
 * change, and the worker takes it back when the hours are up.
 */
function TemporarySquadAccess({
  ask,
  customerId,
  disabled,
  run,
  subscriptionId,
}: {
  ask: (action: PendingAction) => void;
  customerId: string;
  disabled: boolean;
  /** The panel's own action runner, so its pending state and error are the dialog's. */
  run: Run;
  subscriptionId: string;
}) {
  const translate = useTranslations("admin.customers");
  const presets = usePresets();
  const squadId = useId();
  const hoursId = useId();
  const [squad, setSquad] = useState<string[]>([]);
  const [hours, setHours] = useState("24");
  const key = `/v1/panel/customers/${customerId}/subscriptions/${subscriptionId}/squad-access`;
  const { data, mutate } = useSWR<{ items: SquadGrant[] }, ApiError>(key, fetcher);
  const parsedHours = Number.parseInt(hours, 10);
  const active = (data?.items ?? []).filter((grant) => grant.status === "active");

  return (
    <div className="flex flex-col gap-3">
      <div className="grid gap-3 sm:grid-cols-[1fr_8rem]">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={squadId}>{translate("actions.squadAccess")}</Label>
          <SquadPicker
            control={{ id: squadId, "aria-invalid": false }}
            multiple={false}
            onChange={setSquad}
            value={squad}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={hoursId}>{translate("actions.squadAccessHours")}</Label>
          <Input
            id={hoursId}
            inputMode="numeric"
            onChange={(event) => setHours(event.target.value)}
            value={hours}
          />
        </div>
      </div>
      <Button
        className="self-start"
        disabled={disabled || squad.length === 0 || !(parsedHours >= 1 && parsedHours <= 720)}
        onClick={() =>
          ask({
            perform: async (reason) => {
              const ok = await run(key, {
                body: { hours: parsedHours, squadId: squad[0] },
                method: "POST",
                reason,
              });
              if (ok) {
                setSquad([]);
                void mutate();
              }
              return ok;
            },
            presets: presets("squadAccessGrant"),
            title: translate("actions.squadAccess"),
          })
        }
        size="sm"
      >
        {translate("actions.squadAccessGrant")}
      </Button>
      {active.length > 0 && (
        <ul className="flex flex-col gap-1 text-xs">
          {active.map((grant) => (
            <li className="flex items-center justify-between gap-2" key={grant.id}>
              <span className="font-mono">
                {grant.squadId.slice(0, 8)} ·{" "}
                {translate("actions.squadAccessUntil", {
                  until: new Date(grant.endsAt).toLocaleString(),
                })}
              </span>
              <Button
                disabled={disabled}
                onClick={() =>
                  ask({
                    perform: async (reason) => {
                      const ok = await run(`${key}/${grant.id}`, { method: "DELETE", reason });
                      if (ok) {
                        void mutate();
                      }
                      return ok;
                    },
                    destructive: true,
                    presets: presets("squadAccessRevoke"),
                    title: translate("actions.squadAccessRevoke"),
                  })
                }
                size="sm"
                variant="ghost"
              >
                {translate("actions.squadAccessRevoke")}
              </Button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/**
 * Connected devices for one subscription.
 *
 * The hardware identifier never reaches the browser. The list shows what an
 * operator needs to recognise a device — platform, model, when it was last seen
 * — and addresses it by an opaque reference the server resolves back.
 */
export function SubscriptionDevices({
  customerId,
  subscriptionId,
}: {
  customerId: string;
  subscriptionId: string;
}) {
  const translate = useTranslations("admin.customers");
  const presets = usePresets();
  const { can } = useSession();
  const [removing, setRemoving] = useState<PanelDevice | null>(null);
  const { run, pending, error: removeError } = useOperatorAction();

  const base = `/v1/panel/customers/${customerId}/subscriptions/${subscriptionId}/devices`;
  const { data, error, isLoading, mutate } = useSWR<
    Listing<PanelDevice> & {
      sharing?: { level: "none" | "possible" | "likely"; reasons: string[]; activeDevices: number };
    },
    ApiError
  >(base, fetcher);

  if (isLoading) {
    return <Skeleton className="mt-3 h-20 w-full" />;
  }
  if (error) {
    // Remnawave is authoritative for devices, so its being unreachable is a
    // stated limitation of this panel rather than a broken page.
    return <p className="mt-3 text-muted-foreground text-xs">{translate("devices.unavailable")}</p>;
  }

  const items = data?.items ?? [];
  if (items.length === 0) {
    return <p className="mt-3 text-muted-foreground text-xs">{translate("devices.empty")}</p>;
  }

  return (
    <div className="mt-3 flex flex-col gap-2">
      <p className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
        {translate("devices.title")}
      </p>
      {data?.sharing && data.sharing.level !== "none" && (
        // Read-only: Remnawave owns devices, and nothing here acts on the
        // signal. It tells the operator what to look at.
        <p className="text-warning-tint-foreground text-xs">
          {translate(`devices.sharing.${data.sharing.level}`, {
            count: data.sharing.activeDevices,
          })}{" "}
          {data.sharing.reasons
            .map((reason) => translate(`devices.sharing.reasons.${reason}`))
            .join(", ")}
        </p>
      )}
      {items.map((device) => (
        <div className="flex items-center justify-between gap-3 text-sm" key={device.ref}>
          <span className="flex items-center gap-2">
            <Badge variant="neutral">{device.platform || translate("devices.unknown")}</Badge>
            <span className="text-muted-foreground">
              {device.model || "—"}
              {device.osVersion ? ` · ${device.osVersion}` : ""}
            </span>
          </span>
          <span className="flex items-center gap-2">
            <span className="font-mono text-[11px] text-muted-foreground">
              <Timestamp mode="date" value={device.lastSeenAt} />
            </span>
            {can("subscriptions.write") && (
              <Button
                disabled={pending}
                onClick={() => setRemoving(device)}
                size="sm"
                variant="ghost"
              >
                {translate("devices.remove")}
              </Button>
            )}
          </span>
        </div>
      ))}
      <ReasonDialog
        confirmLabel={translate("devices.remove")}
        description={
          removing
            ? `${removing.platform || translate("devices.unknown")} · ${removing.model || "—"}${
                removing.osVersion ? ` · ${removing.osVersion}` : ""
              }`
            : undefined
        }
        destructive
        error={removeError?.message}
        onConfirm={async (reason) => {
          if (
            removing &&
            (await run(`${base}/${removing.ref}`, {
              method: "DELETE",
              reason,
            }))
          ) {
            setRemoving(null);
            await mutate();
          }
        }}
        onOpenChange={(open) => !open && setRemoving(null)}
        open={removing !== null}
        pending={pending}
        presets={presets("deviceRemove")}
        title={translate("devices.removeTitle")}
      />
    </div>
  );
}
