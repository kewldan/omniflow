"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input, Textarea } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { useOperatorAction } from "@/lib/operations";

const ENDPOINT = "/v1/panel/marketing/reminders";
const SEGMENTS = ["no_subscription", "expiring", "expired", "low_balance"] as const;
const ROUTES = ["none", "plans", "connect", "wallet", "support"] as const;

type Reminder = {
  id?: string;
  name: string;
  active: boolean;
  category: "service" | "marketing";
  segment: (typeof SEGMENTS)[number];
  segmentDays: number;
  balanceBelowMinor: number;
  currency: string;
  repeatEveryDays: number;
  maxSends: number;
  titleRu: string;
  bodyRu: string;
  titleEn: string;
  bodyEn: string;
  buttonRoute?: string;
  showInCabinet: boolean;
};

const BLANK: Reminder = {
  name: "",
  active: false,
  category: "service",
  segment: "no_subscription",
  segmentDays: 3,
  balanceBelowMinor: 0,
  currency: "RUB",
  repeatEveryDays: 7,
  maxSends: 1,
  titleRu: "",
  bodyRu: "",
  titleEn: "",
  bodyEn: "",
  showInCabinet: false,
};

/**
 * The reminder constructor: a message sent again to whoever a segment holds,
 * by the bot and as a card in the customer panel, until they leave the segment,
 * dismiss the card, or have had it the set number of times.
 */
export function RemindersCard({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.reminders");
  const { data, isLoading, mutate } = useSWR<{ items: Reminder[] }, ApiError>(ENDPOINT, fetcher);
  const { run, pending } = useOperatorAction();
  const [draft, setDraft] = useState<Reminder | null>(null);

  async function save(reminder: Reminder) {
    const body = { ...reminder, buttonRoute: reminder.buttonRoute || undefined };
    if (await run(ENDPOINT, { body, method: "PUT" })) {
      setDraft(null);
      await mutate();
      toast.success(translate("saved"));
    }
  }

  async function remove(id: string) {
    if (await run(`${ENDPOINT}/${id}`, { method: "DELETE" })) {
      await mutate();
      toast.success(translate("removed"));
    }
  }

  async function audience(id: string) {
    try {
      const result = await apiFetch<{ customers: number }>(`${ENDPOINT}/${id}/audience`);
      toast.info(translate("audience", { count: result.customers }));
    } catch (audienceError) {
      toast.error((audienceError as ApiError).message);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : (
          (data?.items ?? []).map((reminder) => (
            <div
              className="flex flex-wrap items-center justify-between gap-2 rounded-md border bg-well border-border p-3"
              key={reminder.id}
            >
              <div className="min-w-0">
                <p className="truncate font-medium text-sm">{reminder.name}</p>
                <p className="text-muted-foreground text-xs">
                  {translate(`segments.${reminder.segment}`)} ·{" "}
                  {translate("cadence", {
                    days: reminder.repeatEveryDays,
                    times: reminder.maxSends,
                  })}
                </p>
              </div>
              <div className="flex items-center gap-2">
                <Badge variant={reminder.active ? "success" : "neutral"}>
                  {reminder.active ? translate("on") : translate("off")}
                </Badge>
                <Button
                  onClick={() => reminder.id && audience(reminder.id)}
                  size="sm"
                  variant="ghost"
                >
                  {translate("preview")}
                </Button>
                {editable ? (
                  <>
                    <Button onClick={() => setDraft(reminder)} size="sm" variant="ghost">
                      {translate("edit")}
                    </Button>
                    <Button
                      className="text-destructive"
                      disabled={pending}
                      onClick={() => reminder.id && remove(reminder.id)}
                      size="sm"
                      variant="ghost"
                    >
                      {translate("delete")}
                    </Button>
                  </>
                ) : null}
              </div>
            </div>
          ))
        )}
        {draft ? (
          <ReminderForm
            onCancel={() => setDraft(null)}
            onSave={save}
            pending={pending}
            reminder={draft}
          />
        ) : editable ? (
          <Button className="self-start" onClick={() => setDraft(BLANK)} variant="outline">
            {translate("add")}
          </Button>
        ) : null}
      </CardContent>
    </Card>
  );
}

function ReminderForm({
  reminder,
  onSave,
  onCancel,
  pending,
}: {
  reminder: Reminder;
  onSave: (reminder: Reminder) => void;
  onCancel: () => void;
  pending: boolean;
}) {
  const translate = useTranslations("admin.reminders");
  const id = useId();
  const [form, setForm] = useState<Reminder>(reminder);
  const set = <K extends keyof Reminder>(key: K, value: Reminder[K]) =>
    setForm((current) => ({ ...current, [key]: value }));
  const number = (value: string) => Number.parseInt(value.replace(/\D/g, "") || "0", 10);

  return (
    <form
      className="grid gap-3 rounded-md border bg-well border-border p-3 sm:grid-cols-2"
      onSubmit={(event) => {
        event.preventDefault();
        onSave(form);
      }}
    >
      <div className="flex flex-col gap-1.5 sm:col-span-2">
        <Label htmlFor={`${id}-name`}>{translate("name")}</Label>
        <Input
          id={`${id}-name`}
          maxLength={80}
          onChange={(event) => set("name", event.target.value)}
          value={form.name}
        />
      </div>
      <div className="flex flex-col gap-1.5">
        <Label>{translate("segment")}</Label>
        <Select
          onValueChange={(value) => set("segment", value as Reminder["segment"])}
          value={form.segment}
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {SEGMENTS.map((segment) => (
              <SelectItem key={segment} value={segment}>
                {translate(`segments.${segment}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {form.segment === "low_balance" ? (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-balance`}>
            {translate("balanceBelow", { currency: form.currency })}
          </Label>
          <Input
            id={`${id}-balance`}
            inputMode="numeric"
            onChange={(event) => set("balanceBelowMinor", number(event.target.value) * 100)}
            value={String(Math.floor(form.balanceBelowMinor / 100))}
          />
        </div>
      ) : form.segment === "no_subscription" ? null : (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-days`}>{translate(`segmentDays.${form.segment}`)}</Label>
          <Input
            id={`${id}-days`}
            inputMode="numeric"
            onChange={(event) => set("segmentDays", number(event.target.value))}
            value={String(form.segmentDays)}
          />
        </div>
      )}
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-repeat`}>{translate("repeatEveryDays")}</Label>
        <Input
          id={`${id}-repeat`}
          inputMode="numeric"
          onChange={(event) => set("repeatEveryDays", number(event.target.value))}
          value={String(form.repeatEveryDays)}
        />
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-sends`}>{translate("maxSends")}</Label>
        <Input
          id={`${id}-sends`}
          inputMode="numeric"
          onChange={(event) => set("maxSends", number(event.target.value))}
          value={String(form.maxSends)}
        />
      </div>
      <div className="flex flex-col gap-1.5">
        <Label>{translate("category")}</Label>
        <Select
          onValueChange={(value) => set("category", value as Reminder["category"])}
          value={form.category}
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="service">{translate("categories.service")}</SelectItem>
            <SelectItem value="marketing">{translate("categories.marketing")}</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div className="flex flex-col gap-1.5">
        <Label>{translate("button")}</Label>
        <Select
          onValueChange={(value) => set("buttonRoute", value === "none" ? "" : value)}
          value={form.buttonRoute || "none"}
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {ROUTES.map((route) => (
              <SelectItem key={route} value={route}>
                {translate(`routes.${route}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {(["Ru", "En"] as const).map((language) => (
        <div className="flex flex-col gap-1.5" key={language}>
          <Label htmlFor={`${id}-title${language}`}>{translate(`title${language}`)}</Label>
          <Input
            id={`${id}-title${language}`}
            maxLength={120}
            onChange={(event) => set(`title${language}`, event.target.value)}
            value={form[`title${language}`]}
          />
          <Textarea
            aria-label={translate(`body${language}`)}
            maxLength={2000}
            onChange={(event) => set(`body${language}`, event.target.value)}
            rows={4}
            value={form[`body${language}`]}
          />
        </div>
      ))}
      <div className="flex items-center gap-2">
        <Switch
          checked={form.showInCabinet}
          id={`${id}-cabinet`}
          onCheckedChange={(checked) => set("showInCabinet", checked)}
        />
        <Label htmlFor={`${id}-cabinet`}>{translate("showInCabinet")}</Label>
      </div>
      <div className="flex items-center gap-2">
        <Switch
          checked={form.active}
          id={`${id}-active`}
          onCheckedChange={(checked) => set("active", checked)}
        />
        <Label htmlFor={`${id}-active`}>{translate("active")}</Label>
      </div>
      <div className="flex gap-2 sm:col-span-2">
        <Button disabled={pending} type="submit">
          {translate("save")}
        </Button>
        <Button onClick={onCancel} type="button" variant="ghost">
          {translate("cancel")}
        </Button>
      </div>
    </form>
  );
}
