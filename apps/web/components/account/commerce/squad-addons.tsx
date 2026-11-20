"use client";

import { Button } from "@omniflow/ui/button";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { SectionLabel } from "@/components/account/state";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { useSubmission } from "@/lib/idempotency";

import { useProblemMessage } from "./reasons";
import { type CapacityQuote, ChangePreview } from "./traffic-package";

type SquadAddon = {
  packId: string;
  name: string;
  squadCount: number;
  quote: CapacityQuote;
};

/**
 * Removing a paid server (squad) add-on part-way through the period. What is
 * left of it is credited to the wallet, and the servers it alone added leave
 * the subscription within a minute.
 */
export function SquadAddons({
  onChanged,
  subscriptionId,
}: {
  onChanged: () => void;
  subscriptionId: string;
}) {
  const translate = useTranslations("account.capacity");
  const problem = useProblemMessage();
  const submission = useSubmission();
  const [chosen, setChosen] = useState<SquadAddon | null>(null);
  const [busy, setBusy] = useState(false);
  const key = `/v1/account/subscriptions/${subscriptionId}/squad-addons`;
  const { data, mutate } = useSWR<{ items: SquadAddon[] }, ApiError>(key, fetcher);

  if (!data?.items.length) {
    return null;
  }

  async function remove(addon: SquadAddon) {
    setBusy(true);
    const idempotencyKey = submission.begin();
    try {
      await apiFetch(`${key}/${addon.packId}/removal`, {
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

  return (
    <section className="space-y-3 rounded-lg border border-border bg-card p-4">
      <SectionLabel>{translate("squadsTitle")}</SectionLabel>
      <ul className="space-y-2">
        {data.items.map((addon) => (
          <li className="flex items-center justify-between gap-3" key={addon.packId}>
            <span className="text-[13px]">{addon.name}</span>
            <Button disabled={busy} onClick={() => setChosen(addon)} size="sm" variant="outline">
              {translate("squadsRemove")}
            </Button>
          </li>
        ))}
      </ul>
      {chosen && (
        <ChangePreview
          busy={busy}
          onCancel={() => setChosen(null)}
          onConfirm={() => remove(chosen)}
          quote={chosen.quote}
          summary={translate("squadsConfirm", { name: chosen.name })}
        />
      )}
    </section>
  );
}
