"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Switch } from "@omniflow/ui/switch";
import { toast } from "@omniflow/ui/toast";
import { useFormatter, useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import { BrowserNotificationSetting } from "@/components/account/support/browser-notifications";
import { DeliveryHistory } from "@/components/account/support/delivery-history";
import { useProblemMessage } from "@/components/account/support/problem";
import {
  type CommunicationPreferences,
  PREFERENCES_KEY,
  type PreferencesPatch,
} from "@/components/account/support/types";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";

/** The hours a quiet window can start or end on. */
const HOURS = Array.from({ length: 24 }, (_, hour) => hour);

/**
 * The default window offered when quiet hours are switched on.
 *
 * It is a starting point rather than a rule — the two selects below it are what
 * the customer actually sets — but a toggle that turned on and then demanded two
 * more decisions before it meant anything would be a toggle that did nothing.
 */
const DEFAULT_QUIET_WINDOW = { endHour: 8, startHour: 23 };

/**
 * The communication-preferences screen.
 *
 * Every control here writes one field. The route is a genuine PATCH where an
 * omitted field is left untouched, and sending the whole document back would let
 * this page overwrite a choice the customer made in Telegram between the moment
 * this copy was fetched and the moment a switch was flipped.
 *
 * Two things are stated in the copy rather than left to be inferred. Turning off
 * offers, or unsubscribing outright, stops marketing and nothing else — a
 * customer who thinks it also silences an expiry warning will miss the expiry.
 * And the contact list carries flags only: the API never returns an address, and
 * this page does not invent a masked one that would look like it did.
 */
export default function PreferencesPage() {
  const translate = useTranslations("account.support");
  const describeProblem = useProblemMessage();
  const { data, error, isLoading, mutate } = useSWR<CommunicationPreferences, ApiError>(
    PREFERENCES_KEY,
    fetcher,
  );
  const [busy, setBusy] = useState(false);

  async function patch(change: PreferencesPatch) {
    setBusy(true);
    try {
      const next = await apiFetch<CommunicationPreferences>(PREFERENCES_KEY, {
        body: JSON.stringify(change),
        method: "PATCH",
      });
      // The response is the full, authoritative document, so it replaces the
      // cache outright and no revalidation is needed to find out what was stored.
      await mutate(next, { revalidate: false });
      toast.success(translate("preferences.saved"));
    } catch (patchError) {
      toast.error(describeProblem(patchError));
      // The switch rendered from the cache, so restoring the cache is what puts a
      // refused toggle back where it was.
      await mutate();
    } finally {
      setBusy(false);
    }
  }

  if (isLoading) {
    return <ListSkeleton rows={3} />;
  }
  if (error || !data) {
    return (
      <AccountNotice
        action={<Button onClick={() => mutate()}>{translate("actions.retry")}</Button>}
        description={translate("preferences.errorDescription")}
        title={translate("preferences.error")}
        variant={error?.status === 503 ? "offline" : "danger"}
      />
    );
  }

  return (
    <div className="animate-step-in space-y-5">
      <h1 className="font-semibold text-[19px] tracking-[-0.02em]">
        {translate("preferences.title")}
      </h1>

      <SectionLabel>{translate("preferences.notifications.title")}</SectionLabel>
      <div className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-card">
        {(["expiry", "traffic", "renewal", "news"] as const).map((kind) => (
          <ToggleRow
            busy={busy}
            checked={data.notifications[kind]}
            description={translate(`preferences.notifications.${kind}Description`)}
            key={kind}
            label={translate(`preferences.notifications.${kind}`)}
            onChange={(checked) => patch({ notifications: { [kind]: checked } })}
          />
        ))}
      </div>

      <SectionLabel>{translate("preferences.locale.title")}</SectionLabel>
      <MessageLanguage busy={busy} current={data.locale} onChange={patch} />

      <SectionLabel>{translate("preferences.quietHours.title")}</SectionLabel>
      <QuietHours busy={busy} onChange={patch} window={data.quietHours} />

      {/* The card carries its own heading, so it needs no section label above it. */}
      <BrowserNotificationSetting />

      <SectionLabel>{translate("preferences.marketing.title")}</SectionLabel>
      <Marketing busy={busy} onChange={patch} onUnsubscribed={mutate} preferences={data} />

      {data.suppression && <Suppression suppression={data.suppression} />}

      <SectionLabel>{translate("preferences.contacts.title")}</SectionLabel>
      <Contacts contacts={data.contacts} />

      {/* The record sits beneath the settings that produced it. Every reason a
          message did not go out points back at a control above, which is what
          turns "nothing arrived" into something the customer can act on. */}
      <SectionLabel>{translate("history.title")}</SectionLabel>
      <DeliveryHistory />
    </div>
  );
}

/**
 * One labelled switch.
 *
 * The label element wraps the text and points at the switch, so the description
 * beneath is part of the accessible name rather than something only a sighted
 * customer reads before deciding.
 */
function ToggleRow({
  busy,
  checked,
  description,
  label,
  onChange,
}: {
  busy: boolean;
  checked: boolean;
  description: string;
  label: string;
  onChange: (checked: boolean) => void;
}) {
  const switchId = useId();
  const describedBy = useId();
  return (
    <div className="flex items-start gap-3 px-4 py-3.5">
      <div className="min-w-0 flex-1">
        <Label htmlFor={switchId}>{label}</Label>
        <p className="mt-1 text-[12px] text-muted-foreground leading-relaxed" id={describedBy}>
          {description}
        </p>
      </div>
      <Switch
        aria-describedby={describedBy}
        checked={checked}
        disabled={busy}
        id={switchId}
        onCheckedChange={onChange}
      />
    </div>
  );
}

/** The language the bot and the mailer write in, which is not this page's language. */
function MessageLanguage({
  busy,
  current,
  onChange,
}: {
  busy: boolean;
  current: CommunicationPreferences["locale"];
  onChange: (change: PreferencesPatch) => Promise<void>;
}) {
  const translate = useTranslations("account.support");
  const fieldId = useId();
  return (
    <div className="space-y-2 rounded-xl border border-border bg-card p-4">
      <Label htmlFor={fieldId}>{translate("preferences.locale.title")}</Label>
      <Select
        disabled={busy}
        onValueChange={(value) => onChange({ locale: value as CommunicationPreferences["locale"] })}
        value={current}
      >
        <SelectTrigger id={fieldId}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="auto">{translate("preferences.locale.auto")}</SelectItem>
          <SelectItem value="ru">{translate("preferences.locale.ru")}</SelectItem>
          <SelectItem value="en">{translate("preferences.locale.en")}</SelectItem>
        </SelectContent>
      </Select>
      <p className="text-[12px] text-muted-foreground leading-relaxed">
        {translate("preferences.locale.description")}
      </p>
    </div>
  );
}

/**
 * The quiet window.
 *
 * Clearing it is expressed as a window whose hours are equal, which is the
 * convention the API and the bot already share. That is why the off switch sends
 * `{startHour: 0, endHour: 0}` rather than omitting the field: omitting it would
 * mean "leave the window alone", which is the opposite of what the customer just
 * asked for.
 */
function QuietHours({
  busy,
  onChange,
  window,
}: {
  busy: boolean;
  onChange: (change: PreferencesPatch) => Promise<void>;
  window: CommunicationPreferences["quietHours"];
}) {
  const translate = useTranslations("account.support");
  const enabledId = useId();
  const startId = useId();
  const endId = useId();
  const enabled = Boolean(window);

  return (
    <div className="space-y-3 rounded-xl border border-border bg-card p-4">
      <div className="flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <Label htmlFor={enabledId}>{translate("preferences.quietHours.enable")}</Label>
          <p className="mt-1 text-[12px] text-muted-foreground leading-relaxed">
            {translate("preferences.quietHours.description")}
          </p>
        </div>
        <Switch
          checked={enabled}
          disabled={busy}
          id={enabledId}
          onCheckedChange={(checked) =>
            onChange({ quietHours: checked ? DEFAULT_QUIET_WINDOW : { endHour: 0, startHour: 0 } })
          }
        />
      </div>

      {window && (
        <>
          <div className="flex gap-3">
            <div className="flex-1 space-y-1.5">
              <Label htmlFor={startId}>{translate("preferences.quietHours.start")}</Label>
              <Select
                disabled={busy}
                onValueChange={(value) =>
                  onChange({ quietHours: { endHour: window.endHour, startHour: Number(value) } })
                }
                value={String(window.startHour)}
              >
                <SelectTrigger id={startId}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {HOURS.map((hour) => (
                    <SelectItem key={hour} value={String(hour)}>
                      {translate("preferences.quietHours.hour", { hour })}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex-1 space-y-1.5">
              <Label htmlFor={endId}>{translate("preferences.quietHours.end")}</Label>
              <Select
                disabled={busy}
                onValueChange={(value) =>
                  onChange({ quietHours: { endHour: Number(value), startHour: window.startHour } })
                }
                value={String(window.endHour)}
              >
                <SelectTrigger id={endId}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {HOURS.map((hour) => (
                    <SelectItem key={hour} value={String(hour)}>
                      {translate("preferences.quietHours.hour", { hour })}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <p className="font-mono text-[11px] text-subtle-foreground" role="status">
            {translate("preferences.quietHours.active", {
              end: translate("preferences.quietHours.hour", { hour: window.endHour }),
              start: translate("preferences.quietHours.hour", { hour: window.startHour }),
            })}
          </p>
        </>
      )}
      {!window && (
        <p className="font-mono text-[11px] text-subtle-foreground" role="status">
          {translate("preferences.quietHours.inactive")}
        </p>
      )}
    </div>
  );
}

/**
 * Marketing consent and the one-action opt-out.
 *
 * The evidence is shown alongside the switch on purpose. "You opted in" is not a
 * useful thing to tell somebody who does not remember doing so; "you opted in on
 * this date, from this surface, under these terms" is, and it is the same record
 * an auditor would be shown.
 */
function Marketing({
  busy,
  onChange,
  onUnsubscribed,
  preferences,
}: {
  busy: boolean;
  onChange: (change: PreferencesPatch) => Promise<void>;
  onUnsubscribed: (next?: CommunicationPreferences) => Promise<unknown>;
  preferences: CommunicationPreferences;
}) {
  const translate = useTranslations("account.support");
  const describeProblem = useProblemMessage();
  const format = useFormatter();
  const switchId = useId();
  const [confirm, setConfirm] = useState(false);
  const [leaving, setLeaving] = useState(false);
  const { marketing } = preferences;

  async function unsubscribe() {
    setLeaving(true);
    try {
      const next = await apiFetch<CommunicationPreferences>("/v1/account/preferences/unsubscribe", {
        method: "POST",
      });
      await onUnsubscribed(next);
      toast.success(translate("preferences.marketing.unsubscribed"));
    } catch (unsubscribeError) {
      toast.error(describeProblem(unsubscribeError));
    } finally {
      setLeaving(false);
      setConfirm(false);
    }
  }

  const sourceKey = marketing.source ?? "unknown";
  const source = translate.has(`preferences.marketing.source.${sourceKey}`)
    ? translate(`preferences.marketing.source.${sourceKey}`)
    : sourceKey;

  return (
    <div className="space-y-3 rounded-xl border border-border bg-card p-4">
      <div className="flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <Label htmlFor={switchId}>{translate("preferences.marketing.enable")}</Label>
          <p className="mt-1 text-[12px] text-muted-foreground leading-relaxed">
            {translate("preferences.marketing.description")}
          </p>
        </div>
        <Switch
          checked={marketing.enabled}
          disabled={busy || leaving}
          id={switchId}
          onCheckedChange={(checked) => onChange({ marketing: checked })}
        />
      </div>

      <p className="font-mono text-[11px] text-subtle-foreground leading-relaxed">
        {marketing.decidedAt
          ? `${translate("preferences.marketing.decided", {
              date: format.dateTime(new Date(marketing.decidedAt), {
                day: "numeric",
                month: "short",
                year: "numeric",
              }),
              source,
            })} ${
              marketing.policyVersion
                ? translate("preferences.marketing.policyVersion", {
                    version: marketing.policyVersion,
                  })
                : ""
            }`.trim()
          : translate("preferences.marketing.undecided")}
      </p>

      <Button
        className="w-full"
        disabled={busy || leaving}
        onClick={() => setConfirm(true)}
        size="lg"
        variant="outline"
      >
        {translate("preferences.marketing.unsubscribe")}
      </Button>

      <ConfirmDialog
        cancelLabel={translate("actions.cancel")}
        confirmLabel={translate("preferences.marketing.unsubscribe")}
        description={translate("preferences.marketing.unsubscribeConfirmDescription")}
        onConfirm={unsubscribe}
        onOpenChange={setConfirm}
        open={confirm}
        pending={leaving}
        title={translate("preferences.marketing.unsubscribeConfirm")}
      />
    </div>
  );
}

/**
 * The active hold on non-essential messages.
 *
 * It is reported rather than offered as a control: a hold placed by a bounce or a
 * complaint is a finding this screen has no standing to reverse, and the reason
 * is named so the customer knows who to ask.
 */
function Suppression({
  suppression,
}: {
  suppression: NonNullable<CommunicationPreferences["suppression"]>;
}) {
  const translate = useTranslations("account.support");
  const format = useFormatter();
  const reasonKey = `preferences.suppression.reason.${suppression.reason}`;

  return (
    <div
      className="space-y-1 rounded-lg border border-warning/40 bg-warning/10 px-4 py-3"
      role="status"
    >
      <p className="font-semibold text-[13.5px]">{translate("preferences.suppression.title")}</p>
      <p className="text-[12.5px] leading-relaxed">
        {translate.has(reasonKey) ? translate(reasonKey) : ""}{" "}
        {translate("preferences.suppression.note")}
      </p>
      <p className="font-mono text-[11px] text-muted-foreground">
        {translate("preferences.suppression.since", {
          date: format.dateTime(new Date(suppression.createdAt), {
            day: "numeric",
            month: "short",
            year: "numeric",
          }),
        })}
      </p>
    </div>
  );
}

/**
 * The channels we can reach the customer on.
 *
 * Flags only. The API deliberately never returns the address, and rendering a
 * masked stand-in here would put a plausible-looking contact detail on a screen
 * whose whole point is that it does not hold one.
 */
function Contacts({ contacts }: { contacts: CommunicationPreferences["contacts"] }) {
  const translate = useTranslations("account.support");
  const format = useFormatter();

  if (contacts.length === 0) {
    return (
      <AccountNotice
        description={translate("preferences.contacts.emptyDescription")}
        title={translate("preferences.contacts.empty")}
      />
    );
  }

  return (
    <div className="space-y-2">
      <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-card">
        {contacts.map((contact) => (
          <li className="space-y-2 px-4 py-3.5" key={contact.id}>
            <div className="flex items-center justify-between gap-3">
              <span className="font-semibold text-[14px]">
                {translate(`preferences.contacts.kind.${contact.kind}`)}
              </span>
              <span className="shrink-0 font-mono text-[10.5px] text-subtle-foreground">
                {translate("preferences.contacts.added", {
                  date: format.dateTime(new Date(contact.createdAt), {
                    day: "numeric",
                    month: "short",
                    year: "numeric",
                  }),
                })}
              </span>
            </div>
            <div className="flex flex-wrap gap-1.5">
              {/* Verification is its own badge because its two labels already
                  say which state they are. The two purpose flags do not, so they
                  spell the state out rather than leaving it to the colour. */}
              <Badge variant={contact.verified ? "success" : "neutral"}>
                {translate(
                  contact.verified
                    ? "preferences.contacts.verified"
                    : "preferences.contacts.unverified",
                )}
              </Badge>
              <Flag
                active={contact.transactional}
                label={translate("preferences.contacts.transactional")}
              />
              <Flag
                active={contact.marketing}
                label={translate("preferences.contacts.marketing")}
              />
            </div>
          </li>
        ))}
      </ul>
      <p className="px-1 text-[12px] text-muted-foreground leading-relaxed">
        {translate("preferences.contacts.description")}
      </p>
    </div>
  );
}

function Flag({ active, label }: { active: boolean; label: string }) {
  const translate = useTranslations("account.support");
  const state = translate(active ? "preferences.contacts.on" : "preferences.contacts.off");
  return <Badge variant={active ? "success" : "neutral"}>{`${label} · ${state}`}</Badge>;
}
