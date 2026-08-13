"use client";

import { Alert, AlertDescription, AlertTitle } from "@omniflow/ui/alert";
import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { DateTimeField } from "@omniflow/ui/date-time-field";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useLocale, useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { PageHeader } from "@/components/admin/resource-table";
import { type ApiError, fetcher } from "@/lib/api";
import { formatMoney, type Listing, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

import type {
  AudienceSegment,
  Campaign,
  MessageTemplate,
  ReferralProgram,
  Suppression,
} from "./types";

/**
 * Campaigns, segments, suppressions, and the referral programme.
 *
 * The screen is built around the moment before a send. A campaign is created in
 * draft with its estimated audience already visible, scheduling is a second
 * action, and the segment's definition is rendered in words beside the count —
 * because the mistake this page exists to prevent is sending the right message
 * to the wrong people, and nobody catches that by reading a segment code.
 */
export function MarketingWorkspace() {
  const translate = useTranslations("admin.marketing");
  const { can } = useSession();

  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />
      <CampaignCard editable={can("marketing.write")} />
      <SegmentCard />
      <SuppressionCard editable={can("marketing.write")} />
      <ReferralCard editable={can("settings.write")} />
    </div>
  );
}

/** The states an operator can move a campaign into, and from where. */
const TRANSITIONS: Record<string, string[]> = {
  draft: ["scheduled", "cancelled"],
  scheduled: ["paused", "cancelled"],
  paused: ["scheduled", "cancelled"],
};

/**
 * The campaign states a preview can be asked for from.
 *
 * It mirrors the API's own rule rather than replacing it — the server refuses
 * the rest regardless — and exists so the button is absent where it would only
 * ever produce a refusal.
 */
const TESTABLE = ["draft", "scheduled", "paused"];

function CampaignCard({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.marketing");
  const { data, isLoading, mutate } = useSWR<Listing<Campaign>, ApiError>(
    "/v1/panel/marketing/campaigns",
    fetcher,
  );
  const { data: segments } = useSWR<Listing<AudienceSegment>, ApiError>(
    "/v1/panel/marketing/segments",
    fetcher,
  );
  const { data: templates } = useSWR<Listing<MessageTemplate>, ApiError>(
    "/v1/panel/marketing/templates",
    fetcher,
  );
  const { run, pending } = useOperatorAction();

  const [form, setForm] = useState({ name: "", segmentId: "", templateId: "" });
  const [schedule, setSchedule] = useState<Record<string, string>>({});
  // Which language was last previewed, per campaign. It confirms the button did
  // something, because what it did happens in another application.
  const [tested, setTested] = useState<Record<string, string>>({});
  const nameId = useId();
  const scheduleId = useId();

  const campaigns = data?.items ?? [];
  const availableSegments = segments?.items ?? [];
  const availableTemplates = templates?.items ?? [];
  const chosen = availableSegments.find((segment) => segment.id === form.segmentId);

  async function create() {
    const created = await run("/v1/panel/marketing/campaigns", {
      method: "POST",
      body: form,
      reason: translate("campaigns.reason"),
    });
    if (created) {
      setForm({ name: "", segmentId: "", templateId: "" });
      mutate();
    }
  }

  /**
   * Queues one copy of the campaign's message for the operator group.
   *
   * It goes nowhere near the audience: the API records it in its own table, so
   * the counters beside the campaign do not move and nobody is removed from the
   * send by having already "received" it. Delivery is the bot's, so the button
   * reports that the preview was queued rather than that it arrived.
   */
  async function sendTest(campaign: Campaign, locale: "en" | "ru") {
    const queued = await run(`/v1/panel/marketing/campaigns/${campaign.id}/test`, {
      method: "POST",
      body: { locale },
      reason: translate("campaigns.testReason"),
    });
    if (queued) {
      setTested({ ...tested, [campaign.id]: locale });
    }
  }

  async function move(campaign: Campaign, state: string) {
    const moved = await run(`/v1/panel/marketing/campaigns/${campaign.id}/state`, {
      method: "POST",
      body: {
        state,
        scheduledFor:
          state === "scheduled" && schedule[campaign.id]
            ? new Date(schedule[campaign.id]).toISOString()
            : undefined,
      },
      reason: translate("campaigns.reason"),
    });
    if (moved) {
      mutate();
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("campaigns.title")}</CardTitle>
        <CardDescription>{translate("campaigns.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {isLoading ? <Skeleton className="h-32 w-full" /> : null}

        {editable ? (
          <div className="flex flex-col gap-3 rounded-md border border-dashed p-4">
            <p className="font-medium text-sm">{translate("campaigns.newTitle")}</p>
            <div className="grid gap-3 sm:grid-cols-3">
              <div className="flex flex-col gap-1">
                <Label htmlFor={nameId}>{translate("campaigns.name")}</Label>
                <Input
                  id={nameId}
                  onChange={(event) => setForm({ ...form, name: event.target.value })}
                  value={form.name}
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor={`${nameId}-segment`}>{translate("campaigns.segment")}</Label>
                {/* The live size sits in the option itself: choosing an audience
                    without seeing how many people are in it is the mistake this
                    whole screen is arranged to prevent. */}
                <Select
                  onValueChange={(segmentId) => setForm({ ...form, segmentId })}
                  value={form.segmentId}
                >
                  <SelectTrigger id={`${nameId}-segment`}>
                    <SelectValue placeholder={translate("campaigns.choose")} />
                  </SelectTrigger>
                  <SelectContent>
                    {availableSegments.map((segment) => (
                      <SelectItem key={segment.id} value={segment.id}>
                        {segment.nameEn} ({segment.size})
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor={`${nameId}-template`}>{translate("campaigns.template")}</Label>
                <Select
                  onValueChange={(templateId) => setForm({ ...form, templateId })}
                  value={form.templateId}
                >
                  <SelectTrigger id={`${nameId}-template`}>
                    <SelectValue placeholder={translate("campaigns.choose")} />
                  </SelectTrigger>
                  <SelectContent>
                    {availableTemplates.map((template) => (
                      <SelectItem key={template.id} value={template.id}>
                        {template.code}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>

            {/* The preview: who this reaches, in words, before anything is
                created. A segment code tells an operator nothing about whether
                they picked the right one. */}
            {chosen ? (
              <Alert>
                <AlertTitle>{translate("campaigns.preview", { count: chosen.size })}</AlertTitle>
                <AlertDescription>
                  <ul className="list-inside list-disc">
                    {(chosen.explain ?? []).map((line) => (
                      <li key={line}>{line}</li>
                    ))}
                  </ul>
                </AlertDescription>
              </Alert>
            ) : null}

            <Button
              className="self-start"
              disabled={pending || !form.name || !form.segmentId || !form.templateId}
              onClick={create}
              type="button"
            >
              {translate("campaigns.createDraft")}
            </Button>
          </div>
        ) : null}

        {campaigns.length === 0 && !isLoading ? (
          <p className="text-muted-foreground text-sm">{translate("campaigns.empty")}</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("campaigns.name")}</TableHead>
                <TableHead>{translate("campaigns.status")}</TableHead>
                <TableHead className="text-right">{translate("campaigns.audience")}</TableHead>
                <TableHead className="text-right">{translate("campaigns.results")}</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {campaigns.map((campaign) => (
                <TableRow key={campaign.id}>
                  <TableCell>
                    <span className="font-medium">{campaign.name}</span>
                    <p className="font-mono text-muted-foreground text-xs">
                      {campaign.segmentCode} · {campaign.templateCode}
                    </p>
                  </TableCell>
                  <TableCell>
                    <Badge variant={campaign.status === "failed" ? "danger" : "neutral"}>
                      {translate(`campaigns.states.${campaign.status}`)}
                    </Badge>
                    {campaign.scheduledFor ? (
                      <p className="text-muted-foreground text-xs">
                        {new Date(campaign.scheduledFor).toLocaleString()}
                      </p>
                    ) : null}
                  </TableCell>
                  <TableCell className="text-right">{campaign.estimatedAudience}</TableCell>
                  <TableCell className="text-right text-xs">
                    {/* Suppressed is shown beside sent rather than folded into
                        it: "we did not message these people on purpose" is a
                        different fact from a failure. */}
                    {translate("campaigns.counts", {
                      failed: campaign.failedCount,
                      sent: campaign.sentCount,
                      suppressed: campaign.suppressedCount,
                    })}
                  </TableCell>
                  <TableCell className="text-right">
                    {editable ? (
                      <div className="flex flex-wrap items-center justify-end gap-2">
                        {(TRANSITIONS[campaign.status] ?? []).includes("scheduled") ? (
                          <DateTimeField
                            className="w-56"
                            // A campaign cannot be scheduled into the past, and
                            // the calendar refuses it rather than letting the
                            // API do so after the operator has committed.
                            fromDate={new Date()}
                            hourLabel={translate("campaigns.scheduleHour")}
                            id={`${scheduleId}-${campaign.id}`}
                            minuteLabel={translate("campaigns.scheduleMinute")}
                            onChange={(value) => setSchedule({ ...schedule, [campaign.id]: value })}
                            placeholder={translate("campaigns.scheduleFor")}
                            value={schedule[campaign.id] ?? ""}
                          />
                        ) : null}
                        {/* A preview is only offered while the decision to send
                            is still open. Once a campaign has completed or been
                            cancelled its copy in the operator group would read
                            as though it might still go out. */}
                        {TESTABLE.includes(campaign.status)
                          ? (["en", "ru"] as const).map((locale) => (
                              <Button
                                disabled={pending}
                                key={locale}
                                onClick={() => sendTest(campaign, locale)}
                                size="sm"
                                type="button"
                                variant="outline"
                              >
                                {translate("campaigns.actions.test", {
                                  locale: locale.toUpperCase(),
                                })}
                              </Button>
                            ))
                          : null}
                        {tested[campaign.id] ? (
                          <span className="text-muted-foreground text-xs">
                            {translate("campaigns.testQueued", {
                              locale: tested[campaign.id].toUpperCase(),
                            })}
                          </span>
                        ) : null}
                        {(TRANSITIONS[campaign.status] ?? []).map((state) => (
                          <Button
                            disabled={pending || (state === "scheduled" && !schedule[campaign.id])}
                            key={state}
                            onClick={() => move(campaign, state)}
                            size="sm"
                            type="button"
                            variant={state === "cancelled" ? "ghost" : "default"}
                          >
                            {translate(`campaigns.actions.${state}`)}
                          </Button>
                        ))}
                      </div>
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

function SegmentCard() {
  const translate = useTranslations("admin.marketing");
  const { data, isLoading } = useSWR<Listing<AudienceSegment>, ApiError>(
    "/v1/panel/marketing/segments",
    fetcher,
  );
  const segments = data?.items ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("segments.title")}</CardTitle>
        <CardDescription>{translate("segments.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {isLoading ? <Skeleton className="h-24 w-full" /> : null}
        {segments.map((segment) => (
          <div className="flex flex-col gap-1 rounded-md border p-4" key={segment.id}>
            <div className="flex items-center gap-2">
              <span className="font-medium">{segment.nameEn}</span>
              <Badge variant="outline">{segment.code}</Badge>
              <Badge className="ml-auto" variant="neutral">
                {translate("segments.size", { count: segment.size })}
              </Badge>
            </div>
            {/* The definition in words, generated from the filters, so it
                cannot describe something the query does not do. */}
            <ul className="list-inside list-disc text-muted-foreground text-sm">
              {(segment.explain ?? []).map((line) => (
                <li key={line}>{line}</li>
              ))}
            </ul>
          </div>
        ))}
      </CardContent>
    </Card>
  );
}

function SuppressionCard({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.marketing");
  const { data, isLoading, mutate } = useSWR<Listing<Suppression>, ApiError>(
    "/v1/panel/marketing/suppressions",
    fetcher,
  );
  const { run, pending } = useOperatorAction();
  const suppressions = data?.items ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("suppressions.title")}</CardTitle>
        <CardDescription>{translate("suppressions.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        {isLoading ? <Skeleton className="h-20 w-full" /> : null}
        {suppressions.length === 0 && !isLoading ? (
          <p className="text-muted-foreground text-sm">{translate("suppressions.empty")}</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("suppressions.customer")}</TableHead>
                <TableHead>{translate("suppressions.reason")}</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {suppressions.map((suppression) => (
                <TableRow key={suppression.customerId}>
                  <TableCell className="font-mono text-xs">{suppression.customerId}</TableCell>
                  <TableCell>
                    {suppression.reason}
                    {suppression.note ? (
                      <p className="text-muted-foreground text-xs">{suppression.note}</p>
                    ) : null}
                  </TableCell>
                  <TableCell className="text-right">
                    {editable ? (
                      <Button
                        disabled={pending}
                        onClick={async () => {
                          const removed = await run(
                            `/v1/panel/marketing/suppressions/${suppression.customerId}`,
                            { method: "DELETE", reason: translate("suppressions.reasonHeader") },
                          );
                          if (removed) {
                            mutate();
                          }
                        }}
                        size="sm"
                        type="button"
                        variant="ghost"
                      >
                        {translate("suppressions.remove")}
                      </Button>
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

function ReferralCard({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.marketing");
  const locale = useLocale();
  const { data, isLoading, mutate } = useSWR<ReferralProgram, ApiError>(
    "/v1/panel/marketing/referrals",
    fetcher,
  );
  const { run, pending, error } = useOperatorAction();
  const [form, setForm] = useState<ReferralProgram | null>(null);
  const inviterId = useId();
  const inviteeId = useId();
  const capId = useId();
  const validityId = useId();
  const enabledId = useId();

  const program = form ?? data;
  if (isLoading || !program) {
    return <Skeleton className="h-48 w-full" />;
  }

  function update(patch: Partial<ReferralProgram>) {
    setForm({ ...(program as ReferralProgram), ...patch });
  }

  async function save() {
    const saved = await run("/v1/panel/marketing/referrals", {
      method: "PUT",
      body: program,
      reason: translate("referrals.reason"),
    });
    if (saved) {
      setForm(null);
      mutate();
    }
  }

  // Enabling a programme that pays nothing is refused by the API. Saying so
  // here means the operator learns it before pressing save rather than after.
  const payless =
    program.enabled && program.inviterRewardMinor === 0 && program.inviteeRewardMinor === 0;

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("referrals.title")}</CardTitle>
        <CardDescription>{translate("referrals.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex flex-wrap items-center gap-3">
          <Switch
            checked={program.enabled}
            disabled={!editable}
            id={enabledId}
            onCheckedChange={(enabled) => update({ enabled })}
          />
          <Label htmlFor={enabledId}>{translate("referrals.enabled")}</Label>
          <Badge className="ml-auto" variant="outline">
            {program.currency}
          </Badge>
        </div>

        {payless ? (
          <Alert variant="danger">
            <AlertTitle>{translate("referrals.paylessTitle")}</AlertTitle>
            <AlertDescription>{translate("referrals.payless")}</AlertDescription>
          </Alert>
        ) : null}

        <div className="grid gap-3 sm:grid-cols-4">
          <div className="flex flex-col gap-1">
            <Label htmlFor={inviterId}>{translate("referrals.inviterReward")}</Label>
            <Input
              disabled={!editable}
              id={inviterId}
              inputMode="numeric"
              onChange={(event) => update({ inviterRewardMinor: Number(event.target.value) || 0 })}
              value={String(program.inviterRewardMinor)}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={inviteeId}>{translate("referrals.inviteeReward")}</Label>
            <Input
              disabled={!editable}
              id={inviteeId}
              inputMode="numeric"
              onChange={(event) => update({ inviteeRewardMinor: Number(event.target.value) || 0 })}
              value={String(program.inviteeRewardMinor)}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={capId}>{translate("referrals.cap")}</Label>
            {/* Empty means no cap, and an uncapped scheme is the one that gets
                farmed — so the hint says so rather than leaving it blank. */}
            <Input
              disabled={!editable}
              id={capId}
              inputMode="numeric"
              onChange={(event) =>
                update({ inviterRewardCap: Number(event.target.value) || undefined })
              }
              placeholder={translate("referrals.capPlaceholder")}
              value={program.inviterRewardCap ? String(program.inviterRewardCap) : ""}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={validityId}>{translate("referrals.validity")}</Label>
            <Input
              disabled={!editable}
              id={validityId}
              inputMode="numeric"
              onChange={(event) =>
                update({ attributionValidityDays: Number(event.target.value) || 0 })
              }
              value={String(program.attributionValidityDays)}
            />
          </div>
        </div>

        {/* What the current configuration actually produced, so a change is made
            against evidence rather than against a guess. */}
        <div className="grid gap-3 rounded-md border p-4 text-sm sm:grid-cols-4">
          <Figure label={translate("referrals.attributed")} value={program.record.attributed} />
          <Figure label={translate("referrals.qualified")} value={program.record.qualified} />
          <Figure label={translate("referrals.rejected")} value={program.record.rejected} />
          <Figure
            label={translate("referrals.rewarded")}
            value={formatMoney(program.record.rewardedMinor, program.currency, locale)}
          />
        </div>

        {error ? <p className="text-destructive text-sm">{translate("saveFailed")}</p> : null}
        {editable ? (
          <Button
            className="self-start"
            disabled={pending || form === null || payless}
            onClick={save}
            type="button"
          >
            {translate("save")}
          </Button>
        ) : null}
      </CardContent>
    </Card>
  );
}

function Figure({ label, value }: { label: string; value: number | string }) {
  return (
    <div className="flex flex-col">
      <span className="text-muted-foreground text-xs">{label}</span>
      <span className="font-medium tabular-nums">{value}</span>
    </div>
  );
}
