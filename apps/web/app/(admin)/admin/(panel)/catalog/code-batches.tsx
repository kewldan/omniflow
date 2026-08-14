"use client";

import { Alert, AlertDescription, AlertTitle } from "@omniflow/ui/alert";
import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { DateTimeField } from "@omniflow/ui/date-time-field";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { toast } from "@omniflow/ui/toast";
import { Copy, Download, Plus } from "lucide-react";
import { useLocale, useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { PageHeader } from "@/components/admin/resource-table";
import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import type { CodeBatch, CodeBatchList, GeneratedBatch, PlanSummary } from "@/lib/operations";
import { formatMoney, type Listing, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

const ENDPOINT = "/v1/panel/codes/batches";

/**
 * Wholesale code batches: selling a block of access to a distributor.
 *
 * Promo codes discount a purchase the customer still makes. Gifts are issued one
 * at a time through an order. This is the third thing: generate two hundred
 * codes at an agreed price, hand over the list, and be able to kill whatever is
 * left when the list leaks.
 *
 * The screen is shaped by one fact — **the codes exist once**. Only their
 * SHA-256 is stored, so the list on screen after generating is the only copy
 * there will ever be. That is said before the operator generates a batch rather
 * than discovered after, and it is why the result panel stays open until they
 * dismiss it and offers a download rather than a "close" button first.
 */
export function CodeBatchesScreen() {
  const translate = useTranslations("admin.codes");
  const { can } = useSession();
  const editable = can("catalog.write");

  const { data, isLoading, mutate } = useSWR<CodeBatchList, ApiError>(ENDPOINT, fetcher);
  const [generated, setGenerated] = useState<GeneratedBatch | null>(null);
  const [creating, setCreating] = useState(false);

  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />

      {generated ? <GeneratedPanel batch={generated} onDismiss={() => setGenerated(null)} /> : null}

      {editable && !creating && !generated ? (
        <div>
          <Button onClick={() => setCreating(true)} size="sm">
            <Plus aria-hidden />
            {translate("create")}
          </Button>
        </div>
      ) : null}

      {creating ? (
        <BatchForm
          maxBatchSize={data?.maxBatchSize ?? 10000}
          onCancel={() => setCreating(false)}
          onCreated={(result) => {
            setCreating(false);
            setGenerated(result);
            mutate();
          }}
        />
      ) : null}

      {isLoading || !data ? (
        <Skeleton className="h-64 w-full" />
      ) : (
        <BatchList batches={data.items ?? []} editable={editable} onChanged={() => mutate()} />
      )}
    </div>
  );
}

/**
 * The one place a redeemable code is ever shown.
 *
 * It stays until dismissed and the dismissal confirms, because closing it is
 * irreversible in a way no other panel action is: nothing in the database can
 * produce these strings again.
 */
function GeneratedPanel({ batch, onDismiss }: { batch: GeneratedBatch; onDismiss: () => void }) {
  const translate = useTranslations("admin.codes");
  const [confirming, setConfirming] = useState(false);

  const text = batch.codes.join("\n");

  function download() {
    // Built in the browser from the response already in memory. Asking the
    // server for the file again would mean the server had kept them.
    const blob = new Blob([`${text}\n`], { type: "text/plain;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `${batch.batch.reference}.txt`;
    anchor.click();
    URL.revokeObjectURL(url);
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      toast.success(translate("copied"));
    } catch {
      toast.error(translate("copyFailed"));
    }
  }

  return (
    <Alert variant="warning">
      <AlertTitle>{translate("generated.title", { count: batch.codes.length })}</AlertTitle>
      <AlertDescription className="flex flex-col gap-3">
        <p>{translate("generated.onlyCopy")}</p>
        <div className="flex flex-wrap gap-2">
          <Button onClick={download} size="sm" variant="secondary">
            <Download aria-hidden />
            {translate("generated.download")}
          </Button>
          <Button onClick={copy} size="sm" variant="secondary">
            <Copy aria-hidden />
            {translate("generated.copy")}
          </Button>
          <Button onClick={() => setConfirming(true)} size="sm" variant="ghost">
            {translate("generated.dismiss")}
          </Button>
        </div>
        <pre className="max-h-64 overflow-auto rounded-md border border-border bg-background p-3 font-mono text-[11px] leading-relaxed">
          {text}
        </pre>
        <ConfirmDialog
          cancelLabel={translate("cancel")}
          confirmLabel={translate("generated.dismiss")}
          description={translate("generated.dismissWarning")}
          destructive
          onConfirm={() => {
            setConfirming(false);
            onDismiss();
          }}
          onOpenChange={setConfirming}
          open={confirming}
          title={translate("generated.dismissTitle")}
        />
      </AlertDescription>
    </Alert>
  );
}

function BatchForm({
  maxBatchSize,
  onCancel,
  onCreated,
}: {
  maxBatchSize: number;
  onCancel: () => void;
  onCreated: (batch: GeneratedBatch) => void;
}) {
  const translate = useTranslations("admin.codes");
  const [reference, setReference] = useState("");
  const [planVersionId, setPlanVersionId] = useState("");
  const [quantity, setQuantity] = useState("50");
  const [unitPrice, setUnitPrice] = useState("0");
  const [currency, setCurrency] = useState("USD");
  const [note, setNote] = useState("");
  const [expiresAt, setExpiresAt] = useState("");
  const [busy, setBusy] = useState(false);

  const referenceId = useId();
  const quantityId = useId();
  const priceId = useId();
  const currencyId = useId();
  const noteId = useId();
  const expiryId = useId();

  async function submit() {
    setBusy(true);
    try {
      // Not through useOperatorAction: this is the one call whose response body
      // matters, and that hook reports only whether it worked.
      const created = await apiFetch<GeneratedBatch>(ENDPOINT, {
        body: JSON.stringify({
          currency,
          expiresAt: expiresAt ? new Date(expiresAt).toISOString() : undefined,
          note: note.trim() || undefined,
          planVersionId,
          quantity: Number(quantity) || 0,
          reference: reference.trim(),
          unitPriceMinor: Number(unitPrice) || 0,
        }),
        method: "POST",
      });
      onCreated(created);
    } catch (error) {
      toast.error((error as ApiError).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("form.title")}</CardTitle>
        <CardDescription>{translate("form.description", { max: maxBatchSize })}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            hint={translate("form.referenceHint")}
            id={referenceId}
            label={translate("form.reference")}
            onChange={setReference}
            value={reference}
          />
          <PlanVersionPicker onChange={setPlanVersionId} value={planVersionId} />
          <Field
            id={quantityId}
            label={translate("form.quantity")}
            onChange={setQuantity}
            value={quantity}
          />
          <Field
            hint={translate("form.priceHint")}
            id={priceId}
            label={translate("form.price")}
            onChange={setUnitPrice}
            value={unitPrice}
          />
          <Field
            id={currencyId}
            label={translate("form.currency")}
            onChange={(value) => setCurrency(value.toUpperCase())}
            value={currency}
          />
          <div className="flex flex-col gap-2">
            <Label htmlFor={expiryId}>{translate("form.expiry")}</Label>
            <DateTimeField
              hourLabel={translate("form.hour")}
              id={expiryId}
              minuteLabel={translate("form.minute")}
              onChange={setExpiresAt}
              placeholder={translate("form.noExpiry")}
              value={expiresAt}
            />
            <p className="text-subtle-foreground text-xs">{translate("form.expiryHint")}</p>
          </div>
        </div>
        <Field id={noteId} label={translate("form.note")} onChange={setNote} value={note} />

        <div className="flex items-center gap-2">
          <Button disabled={busy || !reference.trim() || !planVersionId} onClick={submit}>
            {translate("form.generate")}
          </Button>
          <Button onClick={onCancel} variant="ghost">
            {translate("cancel")}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

/**
 * The plan version a batch grants.
 *
 * A version rather than a plan, because a batch grants what the version said
 * when it was created: publishing a new version must not change what two
 * hundred people already hold a code for.
 */
function PlanVersionPicker({
  onChange,
  value,
}: {
  onChange: (value: string) => void;
  value: string;
}) {
  const translate = useTranslations("admin.codes");
  const id = useId();
  const { data } = useSWR<Listing<PlanSummary>, ApiError>("/v1/panel/catalog/plans", fetcher);
  const [planId, setPlanId] = useState("");

  const { data: detail } = useSWR<{ versions: { id: string; version: number }[] }, ApiError>(
    planId ? `/v1/panel/catalog/plans/${planId}` : null,
    fetcher,
  );

  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor={id}>{translate("form.plan")}</Label>
      <div className="grid gap-2 sm:grid-cols-2">
        <Select onValueChange={setPlanId} value={planId}>
          <SelectTrigger id={id}>
            <SelectValue placeholder={translate("form.pickPlan")} />
          </SelectTrigger>
          <SelectContent>
            {(data?.items ?? []).map((plan) => (
              <SelectItem key={plan.id} value={plan.id}>
                {plan.code}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select disabled={!planId} onValueChange={onChange} value={value}>
          <SelectTrigger>
            <SelectValue placeholder={translate("form.pickVersion")} />
          </SelectTrigger>
          <SelectContent>
            {(detail?.versions ?? []).map((version) => (
              <SelectItem key={version.id} value={version.id}>
                v{version.version}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    </div>
  );
}

function BatchList({
  batches,
  editable,
  onChanged,
}: {
  batches: CodeBatch[];
  editable: boolean;
  onChanged: () => void;
}) {
  const translate = useTranslations("admin.codes");
  const locale = useLocale();

  if (batches.length === 0) {
    return <StateNotice description={translate("emptyHint")} title={translate("empty")} />;
  }

  return (
    <Card>
      <CardContent className="overflow-x-auto pt-6">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{translate("list.reference")}</TableHead>
              <TableHead>{translate("list.plan")}</TableHead>
              <TableHead className="text-right">{translate("list.unredeemed")}</TableHead>
              <TableHead className="text-right">{translate("list.redeemed")}</TableHead>
              <TableHead className="text-right">{translate("list.revoked")}</TableHead>
              <TableHead className="text-right">{translate("list.price")}</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {batches.map((batch) => (
              <BatchRow
                batch={batch}
                editable={editable}
                key={batch.id}
                locale={locale}
                onChanged={onChanged}
              />
            ))}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}

function BatchRow({
  batch,
  editable,
  locale,
  onChanged,
}: {
  batch: CodeBatch;
  editable: boolean;
  locale: string;
  onChanged: () => void;
}) {
  const translate = useTranslations("admin.codes");
  const { run, pending } = useOperatorAction();
  const [confirming, setConfirming] = useState(false);
  const [reason, setReason] = useState("");

  async function revoke() {
    if (await run(`${ENDPOINT}/${batch.id}/revoke`, { body: { reason }, method: "POST" })) {
      setConfirming(false);
      setReason("");
      onChanged();
      toast.success(translate("revoked"));
    }
  }

  return (
    <TableRow>
      <TableCell>
        {batch.reference}
        {batch.revokedAt ? (
          <Badge className="ml-2" variant="danger">
            {translate("list.revokedBadge")}
          </Badge>
        ) : null}
        {batch.note ? (
          <span className="block text-subtle-foreground text-xs">{batch.note}</span>
        ) : null}
      </TableCell>
      <TableCell className="font-mono text-xs">
        {batch.planCode}
        <span className="text-subtle-foreground"> v{batch.planVersion}</span>
      </TableCell>
      <TableCell className="text-right tabular-nums">{batch.issued}</TableCell>
      <TableCell className="text-right tabular-nums">{batch.redeemed}</TableCell>
      <TableCell className="text-right tabular-nums">{batch.revoked}</TableCell>
      <TableCell className="text-right tabular-nums">
        {formatMoney(batch.unitPriceMinor, batch.currency, locale)}
      </TableCell>
      <TableCell className="text-right">
        {editable && !batch.revokedAt ? (
          <>
            <Button onClick={() => setConfirming(true)} size="sm" variant="ghost">
              {translate("revoke")}
            </Button>
            {/* Revoking kills only the unredeemed codes. Somebody is using the
                subscriptions the redeemed ones produced, and taking those back
                is a different decision the operator has not made here. */}
            <ConfirmDialog
              cancelLabel={translate("cancel")}
              confirmLabel={translate("revoke")}
              description={
                <div className="flex flex-col gap-3">
                  <p>{translate("revokeWarning", { count: batch.issued })}</p>
                  <Input
                    onChange={(event) => setReason(event.target.value)}
                    placeholder={translate("revokeReason")}
                    value={reason}
                  />
                </div>
              }
              destructive
              onConfirm={revoke}
              onOpenChange={setConfirming}
              open={confirming}
              pending={pending}
              title={translate("revokeTitle", { reference: batch.reference })}
            />
          </>
        ) : null}
      </TableCell>
    </TableRow>
  );
}

function Field({
  hint,
  id,
  label,
  onChange,
  value,
}: {
  hint?: string;
  id: string;
  label: string;
  onChange: (value: string) => void;
  value: string;
}) {
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor={id}>{label}</Label>
      <Input id={id} onChange={(event) => onChange(event.target.value)} value={value} />
      {hint ? <p className="text-subtle-foreground text-xs">{hint}</p> : null}
    </div>
  );
}
