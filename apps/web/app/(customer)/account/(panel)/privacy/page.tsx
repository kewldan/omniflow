"use client";

import { useTranslations } from "next-intl";
import useSWR from "swr";

import { ConsentTrail } from "@/components/account/account/consent-trail";
import { ContactChannels } from "@/components/account/account/contact-channels";
import { DataExport } from "@/components/account/account/data-export";
import { DeletionRequest } from "@/components/account/account/deletion-request";
import { RetentionState } from "@/components/account/account/retention-state";
import type { PrivacyOverview } from "@/components/account/account/types";
import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import type { ApiError } from "@/lib/api";
import { fetcher } from "@/lib/api";

/**
 * The personal-data screen.
 *
 * Everything a customer can do about the data this installation holds lives on
 * one page, in the order the questions get asked: what state is my account in,
 * how can you reach me, what have I agreed to, what do you actually hold, and
 * how do I leave. Splitting these across routes would make the last one — the
 * one people arrive angry looking for — the hardest to find.
 *
 * Contact channels load themselves rather than arriving with the overview,
 * because they are the one part of this page an installation can be missing
 * entirely: with no encryption key configured the route answers 503, and that is
 * a state for the section to render, not for the whole screen to fail on.
 */
export default function PrivacyPage() {
  const translate = useTranslations("account.account");
  const { data, error, isLoading, mutate } = useSWR<PrivacyOverview, ApiError>(
    "/v1/account/privacy",
    fetcher,
  );

  if (isLoading) {
    return <ListSkeleton rows={3} />;
  }
  if (error || !data) {
    return (
      <AccountNotice
        description={translate("states.loadErrorDescription")}
        title={translate("states.loadError")}
        variant="danger"
      />
    );
  }

  return (
    <div className="animate-step-in space-y-5">
      <SectionLabel>{translate("retention.title")}</SectionLabel>
      <RetentionState retention={data.retention} />

      <SectionLabel>{translate("contacts.title")}</SectionLabel>
      <ContactChannels />

      <SectionLabel>{translate("consents.title")}</SectionLabel>
      <ConsentTrail consents={data.consents} />

      <SectionLabel>{translate("export.title")}</SectionLabel>
      <DataExport preview={data.export} />

      <SectionLabel>{translate("deletion.title")}</SectionLabel>
      <DeletionRequest deletion={data.deletion} onChanged={() => mutate()} />
    </div>
  );
}
