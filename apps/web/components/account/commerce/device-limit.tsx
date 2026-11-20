"use client";

import { Button } from "@omniflow/ui/button";
import { toast } from "@omniflow/ui/toast";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { SectionLabel } from "@/components/account/state";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { useSubmission } from "@/lib/idempotency";

import { useProblemMessage } from "./reasons";
import { type CapacityQuote, ChangePreview } from "./traffic-package";

type Device = { handle: string; name: string; platform: string; alias?: string; lastSeen: string };
type Option = { deviceLimit: number; excessDevices: number; quote: CapacityQuote };
type Reduction = {
  available: boolean;
  currentLimit: number;
  minimumLimit: number;
  connected: number;
  devices: Device[];
  options: Option[];
};

/**
 * Giving back device slots bought as add-ons part-way through the period.
 *
 * The preview shows the credit and, when more devices are connected than the
 * new limit allows, which ones would be left over. The server refuses the
 * change until they are disconnected; this card only says so.
 */
export function DeviceLimit({
  onChanged,
  subscriptionId,
}: {
  onChanged: () => void;
  subscriptionId: string;
}) {
  const translate = useTranslations("account.capacity");
  const problem = useProblemMessage();
  const submission = useSubmission();
  const [chosen, setChosen] = useState<Option | null>(null);
  const [busy, setBusy] = useState(false);
  const key = `/v1/account/subscriptions/${subscriptionId}/device-limit`;
  const { data, mutate } = useSWR<Reduction, ApiError>(key, fetcher);

  if (!data?.available) {
    return null;
  }

  async function reduce(option: Option) {
    setBusy(true);
    const idempotencyKey = submission.begin();
    try {
      await apiFetch(key, {
        body: JSON.stringify({ deviceLimit: option.deviceLimit }),
        headers: { "Idempotency-Key": idempotencyKey },
        method: "POST",
      });
      submission.settle();
      toast.success(translate("done"));
      void mutate();
      onChanged();
    } catch (failure) {
      submission.settle(failure);
      toast.error(problem(failure));
    } finally {
      setBusy(false);
      setChosen(null);
    }
  }

  // The devices seen least recently are listed last, and are the ones a lower
  // limit would leave over first.
  const leftOver = chosen ? data.devices.slice(chosen.deviceLimit) : [];

  return (
    <section className="space-y-3 rounded-lg border border-border bg-card p-4">
      <SectionLabel>{translate("devicesTitle")}</SectionLabel>
      <p className="text-[12px] text-muted-foreground">
        {translate("devicesCurrent", {
          connected: data.connected,
          limit: data.currentLimit,
          minimum: data.minimumLimit,
        })}
      </p>
      <div className="flex flex-wrap gap-2">
        {data.options.map((option) => (
          <Button
            disabled={busy}
            key={option.deviceLimit}
            onClick={() => setChosen(option)}
            size="sm"
            variant={chosen?.deviceLimit === option.deviceLimit ? "default" : "outline"}
          >
            {translate("devicesOption", { limit: option.deviceLimit })}
          </Button>
        ))}
      </div>
      {chosen && chosen.excessDevices > 0 && (
        <div aria-live="polite" className="space-y-2 rounded-md border border-border p-3">
          <p className="text-[12.5px]">
            {translate("devicesExcess", { count: chosen.excessDevices, limit: chosen.deviceLimit })}
          </p>
          <ul className="list-disc space-y-0.5 pl-5 text-[12.5px] text-muted-foreground">
            {leftOver.map((device) => (
              <li key={device.handle}>{device.alias || device.name || device.platform}</li>
            ))}
          </ul>
          <Button asChild size="sm" variant="outline">
            <Link href="/account/devices">{translate("devicesManage")}</Link>
          </Button>
        </div>
      )}
      {chosen && chosen.excessDevices === 0 && (
        <ChangePreview
          busy={busy}
          onCancel={() => setChosen(null)}
          onConfirm={() => reduce(chosen)}
          quote={chosen.quote}
          summary={translate("devicesConfirm", { limit: chosen.deviceLimit })}
        />
      )}
    </section>
  );
}
