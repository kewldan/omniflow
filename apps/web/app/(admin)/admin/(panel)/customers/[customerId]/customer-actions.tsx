"use client";

import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Input, Textarea } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Switch } from "@omniflow/ui/switch";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import { parseMajorToMinor } from "@/lib/money";
import { useOperatorAction } from "@/lib/operations";

/**
 * Credit or debit the customer's wallet from the card. Each submission carries
 * its own idempotency key, so a double click is one adjustment; a debit the
 * balance cannot cover is refused by the API rather than going negative.
 */
export function WalletAdjustmentCard({
  customerId,
  onAdjusted,
}: {
  customerId: string;
  onAdjusted: () => void;
}) {
  const translate = useTranslations("admin.customers.actions.wallet");
  const ids = { amount: useId(), currency: useId(), reason: useId(), notify: useId() };
  const [amount, setAmount] = useState("");
  const [currency, setCurrency] = useState("RUB");
  const [reason, setReason] = useState("");
  const [notify, setNotify] = useState(true);
  const { run, pending, error } = useOperatorAction();

  const parsed = parseMajorToMinor(amount.replace("−", "-"), currency, { allowNegative: true });
  const minor = parsed.ok ? parsed.minor : null;
  const valid =
    minor !== null && minor !== 0 && reason.trim() !== "" && currency.trim().length === 3;

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!valid || minor === null) {
      return;
    }
    const ok = await run(`/v1/panel/customers/${customerId}/wallet-adjustments`, {
      body: {
        amountMinor: minor,
        currency: currency.trim().toUpperCase(),
        reason: reason.trim(),
        notify,
        idempotencyKey: crypto.randomUUID(),
      },
      method: "POST",
      reason: reason.trim(),
    });
    if (ok) {
      toast.success(translate("done"));
      setAmount("");
      setReason("");
      onAdjusted();
    }
  }

  return (
    <Card className="p-4">
      <form className="flex flex-col gap-3" onSubmit={submit}>
        <div className="font-medium">{translate("title")}</div>
        <p className="text-muted-foreground text-sm">{translate("description")}</p>
        <div className="grid gap-3 sm:grid-cols-[1fr_6rem]">
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.amount}>{translate("amount")}</Label>
            <Input
              id={ids.amount}
              inputMode="decimal"
              onChange={(event) => setAmount(event.target.value)}
              placeholder="150 / -50"
              value={amount}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.currency}>{translate("currency")}</Label>
            <Input
              id={ids.currency}
              maxLength={3}
              onChange={(event) => setCurrency(event.target.value.toUpperCase())}
              value={currency}
            />
          </div>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.reason}>{translate("reason")}</Label>
          <Input
            id={ids.reason}
            onChange={(event) => setReason(event.target.value)}
            value={reason}
          />
        </div>
        <div className="flex items-center gap-2">
          <Switch checked={notify} id={ids.notify} onCheckedChange={setNotify} />
          <Label htmlFor={ids.notify}>{translate("notify")}</Label>
        </div>
        {error && (
          <p className="text-destructive text-sm" role="alert">
            {error.code === "insufficient_balance"
              ? translate("insufficient")
              : translate("failed")}
          </p>
        )}
        <Button className="self-start" disabled={!valid || pending} type="submit">
          {translate("submit")}
        </Button>
      </form>
    </Card>
  );
}

/** Write to the customer in Telegram, from the operator's own words. */
export function MessageCustomerCard({ customerId }: { customerId: string }) {
  const translate = useTranslations("admin.customers.actions.message");
  const textId = useId();
  const [text, setText] = useState("");
  const { run, pending, error } = useOperatorAction();

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const ok = await run(`/v1/panel/customers/${customerId}/messages`, {
      body: { text: text.trim() },
      method: "POST",
      reason: "customer message",
    });
    if (ok) {
      toast.success(translate("queued"));
      setText("");
    }
  }

  return (
    <Card className="p-4">
      <form className="flex flex-col gap-3" onSubmit={submit}>
        <div className="font-medium">{translate("title")}</div>
        <p className="text-muted-foreground text-sm">{translate("description")}</p>
        <Label className="sr-only" htmlFor={textId}>
          {translate("title")}
        </Label>
        <Textarea
          id={textId}
          maxLength={3500}
          onChange={(event) => setText(event.target.value)}
          rows={4}
          value={text}
        />
        {error && (
          <p className="text-destructive text-sm" role="alert">
            {translate("failed")}
          </p>
        )}
        <Button className="self-start" disabled={pending || text.trim() === ""} type="submit">
          {translate("submit")}
        </Button>
      </form>
    </Card>
  );
}
