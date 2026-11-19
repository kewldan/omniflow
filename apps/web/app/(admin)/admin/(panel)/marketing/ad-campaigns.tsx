"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useLocale, useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";
import { type ApiError, fetcher } from "@/lib/api";
import { formatMoney, useOperatorAction } from "@/lib/operations";

type AdCampaign = {
  id: string;
  code: string;
  name: string;
  bonusKind: "none" | "wallet" | "days";
  bonusValue: number;
  currency: string;
  active: boolean;
  registrations: number;
  trials: number;
  paying: number;
  revenueMinor: number;
};

/**
 * Advertising campaigns addressed by a Telegram /start parameter: a link
 * t.me/<bot>?start=c_<code> credits the campaign with every new customer it
 * brings, and the table reads what those customers went on to do.
 */
export function AdCampaignsCard({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.marketing.adCampaigns");
  const locale = useLocale();
  const { data, isLoading, mutate } = useSWR<{ items: AdCampaign[] }, ApiError>(
    "/v1/panel/marketing/ad-campaigns",
    fetcher,
  );
  const { run, pending } = useOperatorAction();
  const ids = { code: useId(), name: useId(), kind: useId(), value: useId() };
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [bonusKind, setBonusKind] = useState<AdCampaign["bonusKind"]>("none");
  const [bonusValue, setBonusValue] = useState("");

  async function save(campaign: Partial<AdCampaign>) {
    const ok = await run("/v1/panel/marketing/ad-campaigns", {
      body: { currency: "RUB", active: true, ...campaign },
      method: "PUT",
      reason: "ad campaign",
    });
    if (ok) {
      void mutate();
    }
    return ok;
  }

  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const value = Number(bonusValue) || 0;
    const ok = await save({
      code: code.trim().toLowerCase(),
      name: name.trim(),
      bonusKind,
      bonusValue: bonusKind === "wallet" ? Math.round(value * 100) : value,
    });
    if (ok) {
      setCode("");
      setName("");
      setBonusValue("");
      setBonusKind("none");
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : (data?.items ?? []).length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("empty")}</p>
        ) : (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("columns.campaign")}</TableHead>
                  <TableHead>{translate("columns.link")}</TableHead>
                  <TableHead className="text-right">{translate("columns.registrations")}</TableHead>
                  <TableHead className="text-right">{translate("columns.trials")}</TableHead>
                  <TableHead className="text-right">{translate("columns.paying")}</TableHead>
                  <TableHead className="text-right">{translate("columns.revenue")}</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {data?.items.map((campaign) => (
                  <TableRow key={campaign.id}>
                    <TableCell>
                      <div className="font-medium">{campaign.name}</div>
                      <div className="text-muted-foreground text-xs">
                        {campaign.bonusKind === "none"
                          ? translate("bonus.none")
                          : campaign.bonusKind === "wallet"
                            ? translate("bonus.wallet", {
                                amount: formatMoney(campaign.bonusValue, campaign.currency, locale),
                              })
                            : translate("bonus.days", { days: campaign.bonusValue })}
                      </div>
                    </TableCell>
                    <TableCell className="font-mono text-xs">?start=c_{campaign.code}</TableCell>
                    <TableCell className="text-right">{campaign.registrations}</TableCell>
                    <TableCell className="text-right">{campaign.trials}</TableCell>
                    <TableCell className="text-right">{campaign.paying}</TableCell>
                    <TableCell className="text-right">
                      {formatMoney(campaign.revenueMinor, campaign.currency, locale)}
                    </TableCell>
                    <TableCell className="text-right">
                      {editable ? (
                        <Button
                          disabled={pending}
                          onClick={() => void save({ ...campaign, active: !campaign.active })}
                          size="sm"
                          variant="outline"
                        >
                          {campaign.active ? translate("pause") : translate("resume")}
                        </Button>
                      ) : (
                        <Badge variant="outline">
                          {campaign.active ? translate("active") : translate("paused")}
                        </Badge>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}

        {editable && (
          <form className="grid gap-3 sm:grid-cols-4" onSubmit={create}>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.code}>{translate("code")}</Label>
              <Input
                id={ids.code}
                onChange={(event) => setCode(event.target.value)}
                placeholder="stars_elite"
                value={code}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.name}>{translate("name")}</Label>
              <Input id={ids.name} onChange={(event) => setName(event.target.value)} value={name} />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.kind}>{translate("bonusLabel")}</Label>
              <Select
                onValueChange={(value) => setBonusKind(value as AdCampaign["bonusKind"])}
                value={bonusKind}
              >
                <SelectTrigger id={ids.kind}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">{translate("bonusKinds.none")}</SelectItem>
                  <SelectItem value="wallet">{translate("bonusKinds.wallet")}</SelectItem>
                  <SelectItem value="days">{translate("bonusKinds.days")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.value}>{translate("bonusValue")}</Label>
              <Input
                disabled={bonusKind === "none"}
                id={ids.value}
                inputMode="decimal"
                onChange={(event) => setBonusValue(event.target.value)}
                value={bonusValue}
              />
            </div>
            <Button
              className="self-start"
              disabled={pending || !code.trim() || !name.trim()}
              type="submit"
            >
              {translate("create")}
            </Button>
          </form>
        )}
      </CardContent>
    </Card>
  );
}
