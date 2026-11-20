"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Switch } from "@omniflow/ui/switch";
import { useLocale, useTranslations } from "next-intl";
import { useEffect, useId, useRef, useState } from "react";
import useSWR from "swr";

import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { newIdempotencyKey } from "@/lib/idempotency";
import { formatMoney } from "@/lib/operations";

type Plan = { planVersionId: string; name: string; currency: string; amountMinor: number };
type Started = {
  id: string;
  token?: string;
  status: "pending" | "paid" | "mailed" | "expired";
  checkoutUrl?: string;
};

/** Where the status token is kept between the payment page and the return. */
const TOKEN_KEY = "omniflow_guest_purchase";

function readSaved(): { id: string; token: string } | null {
  try {
    const raw = window.localStorage.getItem(TOKEN_KEY);
    return raw ? (JSON.parse(raw) as { id: string; token: string }) : null;
  } catch {
    return null;
  }
}

export function GuestCheckout({ purchaseId }: { purchaseId: string }) {
  const translate = useTranslations("public.guest");
  const locale = useLocale();
  const ids = {
    plan: useId(),
    email: useId(),
    gift: useId(),
    recipient: useId(),
    provider: useId(),
  };
  const { data: options } = useSWR<{ currency: string; providers: string[] }, ApiError>(
    "/v1/account/guest/options",
    fetcher,
  );
  const { data: plans } = useSWR<{ items: Plan[] }, ApiError>(
    `/v1/catalog/plans?locale=${locale === "ru" ? "ru" : "en"}`,
    fetcher,
  );
  const [plan, setPlan] = useState("");
  const [email, setEmail] = useState("");
  const [gift, setGift] = useState(false);
  const [recipient, setRecipient] = useState("");
  const [provider, setProvider] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<Started | null>(null);
  const key = useRef<string | null>(null);

  useEffect(() => {
    const saved = readSaved();
    if (!purchaseId || !saved || saved.id !== purchaseId) {
      return;
    }
    apiFetch<Started>(`/v1/account/guest/purchases/${saved.id}?token=${saved.token}`)
      .then(setStatus)
      .catch(() => setStatus(null));
  }, [purchaseId]);

  const offered = (plans?.items ?? []).filter((item) => item.currency === options?.currency);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError(null);
    key.current ??= newIdempotencyKey();
    try {
      const started = await apiFetch<Started>("/v1/account/guest/purchases", {
        body: JSON.stringify({
          email: email.trim(),
          gift,
          locale,
          planVersionId: plan,
          provider,
          recipientEmail: gift ? recipient.trim() : "",
        }),
        headers: { "Idempotency-Key": key.current },
        method: "POST",
      });
      if (started.token) {
        try {
          window.localStorage.setItem(
            TOKEN_KEY,
            JSON.stringify({ id: started.id, token: started.token }),
          );
        } catch {
          // Without storage the page cannot show the status on return; the
          // e-mail still arrives.
        }
      }
      if (started.checkoutUrl) {
        window.location.assign(started.checkoutUrl);
        return;
      }
      setStatus(started);
    } catch (failure) {
      key.current = null;
      setError((failure as ApiError).message ?? translate("failed"));
    } finally {
      setBusy(false);
    }
  }

  if (status) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>{translate("title")}</CardTitle>
          <CardDescription aria-live="polite">
            {translate(`status.${status.status}`)}
          </CardDescription>
        </CardHeader>
        {status.status === "pending" && status.checkoutUrl && (
          <CardContent>
            <Button asChild>
              <a href={status.checkoutUrl}>{translate("pay")}</a>
            </Button>
          </CardContent>
        )}
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent>
        {(options?.providers ?? []).length === 0 || offered.length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("unavailable")}</p>
        ) : (
          <form className="flex flex-col gap-3" onSubmit={submit}>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.plan}>{translate("plan")}</Label>
              <Select onValueChange={setPlan} value={plan}>
                <SelectTrigger id={ids.plan}>
                  <SelectValue placeholder={translate("choose")} />
                </SelectTrigger>
                <SelectContent>
                  {offered.map((item) => (
                    <SelectItem key={item.planVersionId} value={item.planVersionId}>
                      {item.name} · {formatMoney(item.amountMinor, item.currency, locale)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.email}>{translate("email")}</Label>
              <Input
                autoComplete="email"
                id={ids.email}
                inputMode="email"
                onChange={(event) => setEmail(event.target.value)}
                type="email"
                value={email}
              />
            </div>
            <div className="flex items-center gap-2">
              <Switch checked={gift} id={ids.gift} onCheckedChange={setGift} />
              <Label htmlFor={ids.gift}>{translate("gift")}</Label>
            </div>
            {gift && (
              <div className="flex flex-col gap-1">
                <Label htmlFor={ids.recipient}>{translate("recipient")}</Label>
                <Input
                  id={ids.recipient}
                  inputMode="email"
                  onChange={(event) => setRecipient(event.target.value)}
                  type="email"
                  value={recipient}
                />
                <p className="text-muted-foreground text-xs">{translate("recipientHint")}</p>
              </div>
            )}
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.provider}>{translate("provider")}</Label>
              <Select onValueChange={setProvider} value={provider}>
                <SelectTrigger id={ids.provider}>
                  <SelectValue placeholder={translate("choose")} />
                </SelectTrigger>
                <SelectContent>
                  {(options?.providers ?? []).map((name) => (
                    <SelectItem key={name} value={name}>
                      {name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <p className="text-muted-foreground text-xs">{translate("howItWorks")}</p>
            {error && <p className="text-danger-foreground text-sm">{error}</p>}
            <Button disabled={busy || !plan || !provider || !email.includes("@")} type="submit">
              {translate("submit")}
            </Button>
          </form>
        )}
      </CardContent>
    </Card>
  );
}
