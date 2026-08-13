"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { DateTimeField } from "@omniflow/ui/date-time-field";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";
import { type Listing, type Promotion, useOperatorAction } from "@/lib/operations";

import { OfferPreview } from "./offer-preview";

/**
 * Creates one targeted offer.
 *
 * Both locales are required, and the form says so rather than accepting one and
 * failing at the server: an offer with copy in a single language renders as a
 * blank card for half the customer base, and the bot has no sensible fallback
 * to invent.
 *
 * The discount itself is not defined here. An offer points at an existing
 * promotion, so the stacking rules, precedence, and rejection reasons that
 * already govern promotions govern this too — a second, offer-only discount
 * engine would be a second set of rules to keep consistent.
 */
export function OfferComposer({ onCreated }: { onCreated: () => void }) {
  const translate = useTranslations("admin.offers");
  const customerFieldId = useId();
  const promotionFieldId = useId();
  const titleRuFieldId = useId();
  const titleEnFieldId = useId();
  const termsRuFieldId = useId();
  const termsEnFieldId = useId();
  const startsFieldId = useId();
  const expiresFieldId = useId();
  const reasonFieldId = useId();

  const [customerId, setCustomerId] = useState("");
  const [promotionId, setPromotionId] = useState("");
  const [titleRu, setTitleRu] = useState("");
  const [titleEn, setTitleEn] = useState("");
  const [termsRu, setTermsRu] = useState("");
  const [termsEn, setTermsEn] = useState("");
  const [startsAt, setStartsAt] = useState("");
  const [expiresAt, setExpiresAt] = useState("");
  const [reason, setReason] = useState("");

  const { data: promotions } = useSWR<Listing<Promotion>, ApiError>(
    "/v1/panel/catalog/promotions?pageSize=100",
    fetcher,
  );
  const { run, pending, error } = useOperatorAction();

  const ready =
    customerId.trim().length > 0 &&
    promotionId.trim().length > 0 &&
    titleRu.trim().length > 0 &&
    titleEn.trim().length > 0 &&
    expiresAt !== "" &&
    reason.trim().length > 0 &&
    !pending;

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("composer.title")}</CardTitle>
        <CardDescription>{translate("composer.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={customerFieldId}>{translate("composer.customer")}</Label>
            <Input
              id={customerFieldId}
              onChange={(event) => setCustomerId(event.target.value)}
              placeholder={translate("composer.customerHint")}
              value={customerId}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={promotionFieldId}>{translate("composer.promotion")}</Label>
            <Select onValueChange={setPromotionId} value={promotionId}>
              <SelectTrigger id={promotionFieldId}>
                <SelectValue placeholder={translate("composer.promotionHint")} />
              </SelectTrigger>
              <SelectContent>
                {(promotions?.items ?? []).map((promotion) => (
                  <SelectItem key={promotion.id} value={promotion.id}>
                    {promotion.code}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={titleEnFieldId}>{translate("composer.titleEn")}</Label>
            <Input
              id={titleEnFieldId}
              maxLength={120}
              onChange={(event) => setTitleEn(event.target.value)}
              value={titleEn}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={titleRuFieldId}>{translate("composer.titleRu")}</Label>
            <Input
              id={titleRuFieldId}
              maxLength={120}
              onChange={(event) => setTitleRu(event.target.value)}
              value={titleRu}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={termsEnFieldId}>{translate("composer.termsEn")}</Label>
            <Input
              id={termsEnFieldId}
              maxLength={400}
              onChange={(event) => setTermsEn(event.target.value)}
              value={termsEn}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={termsRuFieldId}>{translate("composer.termsRu")}</Label>
            <Input
              id={termsRuFieldId}
              maxLength={400}
              onChange={(event) => setTermsRu(event.target.value)}
              value={termsRu}
            />
          </div>
        </div>

        <div className="grid gap-3 sm:grid-cols-3">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={startsFieldId}>{translate("composer.startsAt")}</Label>
            <DateTimeField
              hourLabel={translate("composer.hour")}
              id={startsFieldId}
              minuteLabel={translate("composer.minute")}
              onChange={setStartsAt}
              placeholder={translate("composer.pickMoment")}
              value={startsAt}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={expiresFieldId}>{translate("composer.expiresAt")}</Label>
            <DateTimeField
              // An offer cannot expire before it starts, so the calendar refuses
              // the days that would; leaving it to the API would mean the
              // operator finds out after filling the rest of the form.
              fromDate={startsAt ? new Date(startsAt) : undefined}
              hourLabel={translate("composer.hour")}
              id={expiresFieldId}
              minuteLabel={translate("composer.minute")}
              onChange={setExpiresAt}
              placeholder={translate("composer.pickMoment")}
              value={expiresAt}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={reasonFieldId}>{translate("composer.reason")}</Label>
            <Input
              id={reasonFieldId}
              onChange={(event) => setReason(event.target.value)}
              placeholder={translate("composer.reasonHint")}
              value={reason}
            />
          </div>
        </div>

        {/* What the customer will actually see, in both languages, before it is
            sent to them. An offer is a promise about price; showing the operator
            the promise as the customer will read it is cheaper than correcting
            it afterwards. */}
        <OfferPreview
          expiresAt={expiresAt}
          termsEn={termsEn}
          termsRu={termsRu}
          titleEn={titleEn}
          titleRu={titleRu}
        />

        <p className="text-muted-foreground text-xs">{translate("composer.singleUse")}</p>
        {error && <p className="text-danger-foreground text-sm">{error.message}</p>}
        <Button
          className="self-start"
          disabled={!ready}
          onClick={async () => {
            const ok = await run("/v1/panel/offers", {
              body: {
                customerId: customerId.trim(),
                expiresAt: new Date(expiresAt).toISOString(),
                promotionId,
                startsAt: startsAt === "" ? undefined : new Date(startsAt).toISOString(),
                termsEn: termsEn.trim(),
                termsRu: termsRu.trim(),
                titleEn: titleEn.trim(),
                titleRu: titleRu.trim(),
              },
              method: "POST",
              reason: reason.trim(),
            });
            if (ok) {
              onCreated();
            }
          }}
          size="sm"
        >
          {translate("composer.submit")}
        </Button>
      </CardContent>
    </Card>
  );
}
