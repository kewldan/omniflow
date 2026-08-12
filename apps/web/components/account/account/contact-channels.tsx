"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Switch } from "@omniflow/ui/switch";
import { toast } from "@omniflow/ui/toast";
import { AtSign, Phone, Send } from "lucide-react";
import { useFormatter, useTranslations } from "next-intl";
import { useId, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import useSWR from "swr";

import { useProblemMessage } from "@/components/account/account/problem";
import type { ContactChannel, ContactKind } from "@/components/account/account/types";
import { AccountNotice, ListSkeleton } from "@/components/account/state";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";

const KIND_ICON = { email: AtSign, phone: Phone, telegram: Send } as const;

/*
 * The value grammars, mirrored from `internal/accountreferral/contacts.go`.
 *
 * They are a courtesy, not a gate: the server normalises and validates the same
 * value again, and its answer is the one that counts. Checking here only saves
 * the customer a round trip to be told about a missing "@", and each pattern is
 * as loose as the server's — a strict address grammar rejects valid addresses
 * more often than it catches invalid ones.
 */
const PATTERNS: Record<ContactKind, RegExp> = {
  email: /^[^@\s]+@[^@\s.]+\.[^@\s]+$/,
  phone: /^\+?[1-9][0-9]{6,14}$/,
  telegram: /^[A-Za-z][A-Za-z0-9_]{4,31}$/,
};

/** Applies the same normalisation the server does before matching. */
function normalize(kind: ContactKind, value: string): string {
  const trimmed = value.trim();
  if (kind === "email") {
    return trimmed.toLowerCase();
  }
  if (kind === "phone") {
    return trimmed.replace(/[\s\-()]/g, "");
  }
  return trimmed.replace(/^@/, "").toLowerCase();
}

type ContactValues = {
  kind: ContactKind;
  marketing: boolean;
  transactional: boolean;
  value: string;
};

/**
 * The form's own rules, expressed as React Hook Form validators.
 *
 * They were a Zod schema first, and Zod earned its place in this repo at trust
 * boundaries — but the trust boundary for a contact channel is the API, which
 * re-checks every one of these and owns the refusal. Carrying a schema
 * validator into the browser for four fields cost this route more first-load
 * JavaScript than any other customer screen, on a panel that is opened on a
 * phone. The rules below are the same rules; only the place they are written
 * changed.
 */
const CONTACT_RULES = {
  transactional: {
    // A channel that may receive nothing is an address stored for no reason, and
    // the server refuses it. Refusing it here too means the customer finds out
    // while the switches are still under their thumb.
    validate: (transactional: boolean, values: ContactValues) => transactional || values.marketing,
  },
  value: {
    maxLength: 254,
    required: true,
    validate: (value: string, values: ContactValues) =>
      PATTERNS[values.kind].test(normalize(values.kind, value)),
  },
} as const;

/**
 * The addresses this installation may reach the customer on.
 *
 * Adding one records an intention and nothing more: the panel cannot prove an
 * address belongs to the person typing it, so the flags here are a permission
 * the delivery pipeline reads, not a claim that the address is verified. The
 * list says which channels were proved and which were not.
 */
export function ContactChannels() {
  const translate = useTranslations("account.account");
  const format = useFormatter();
  const describeProblem = useProblemMessage();
  const { data, error, isLoading, mutate } = useSWR<{ items: ContactChannel[] }, ApiError>(
    "/v1/account/contacts",
    fetcher,
  );
  const [pendingRemoval, setPendingRemoval] = useState<ContactChannel | null>(null);
  const [busy, setBusy] = useState(false);

  async function remove(contact: ContactChannel) {
    setBusy(true);
    try {
      await apiFetch(`/v1/account/contacts/${encodeURIComponent(contact.id)}`, {
        method: "DELETE",
      });
      await mutate();
      toast.success(translate("contacts.removed"));
    } catch (removeError) {
      toast.error(describeProblem(removeError));
    } finally {
      setBusy(false);
      setPendingRemoval(null);
    }
  }

  if (isLoading) {
    return <ListSkeleton rows={2} />;
  }
  if (error) {
    // 503 is the installation having no contact encryption key rather than a
    // fault, so it reads as "not offered here" instead of "try again".
    const unavailable = error.status === 503;
    return (
      <AccountNotice
        description={
          unavailable
            ? translate("errors.contacts_unavailable")
            : translate("states.loadErrorDescription")
        }
        title={unavailable ? translate("contacts.unavailable") : translate("states.loadError")}
        variant={unavailable ? "offline" : "danger"}
      />
    );
  }

  const contacts = data?.items ?? [];
  return (
    <div className="space-y-3">
      {contacts.length === 0 ? (
        <AccountNotice
          description={translate("contacts.emptyDescription")}
          title={translate("contacts.empty")}
        />
      ) : (
        <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-card">
          {contacts.map((contact) => {
            const Icon = KIND_ICON[contact.kind];
            return (
              <li className="flex items-start gap-3 px-4 py-3.5" key={contact.id}>
                <span className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-md bg-muted">
                  <Icon aria-hidden className="size-[15px] text-muted-foreground" />
                </span>
                <div className="min-w-0 flex-1">
                  <p className="truncate font-medium text-[14px]">
                    {contact.value || translate("contacts.valueUnavailable")}
                  </p>
                  <p className="mt-0.5 font-mono text-[11px] text-subtle-foreground">
                    {format.dateTime(new Date(contact.createdAt), {
                      day: "numeric",
                      month: "short",
                      year: "numeric",
                    })}
                  </p>
                  <div className="mt-1.5 flex flex-wrap gap-1.5">
                    <Badge variant={contact.verified ? "success" : "outline"}>
                      {translate(contact.verified ? "contacts.verified" : "contacts.unverified")}
                    </Badge>
                    {contact.transactional && (
                      <Badge variant="neutral">{translate("contacts.transactional")}</Badge>
                    )}
                    {contact.marketing && (
                      <Badge variant="neutral">{translate("contacts.marketing")}</Badge>
                    )}
                  </div>
                </div>
                <Button
                  className="shrink-0 text-destructive"
                  disabled={busy}
                  onClick={() => setPendingRemoval(contact)}
                  size="sm"
                  variant="ghost"
                >
                  {translate("contacts.remove")}
                </Button>
              </li>
            );
          })}
        </ul>
      )}

      <AddContactForm onAdded={() => mutate()} />

      <ConfirmDialog
        cancelLabel={translate("actions.cancel")}
        confirmLabel={translate("contacts.remove")}
        description={translate("contacts.removeDescription")}
        destructive
        onConfirm={() => pendingRemoval && remove(pendingRemoval)}
        onOpenChange={(open) => !open && setPendingRemoval(null)}
        open={pendingRemoval !== null}
        pending={busy}
        title={translate("contacts.removeTitle", {
          value: pendingRemoval?.value || translate("contacts.valueUnavailable"),
        })}
      />
    </div>
  );
}

/** The add form: one address, and what it is allowed to receive. */
function AddContactForm({ onAdded }: { onAdded: () => void }) {
  const translate = useTranslations("account.account");
  const describeProblem = useProblemMessage();
  const kindId = useId();
  const valueId = useId();
  const [failure, setFailure] = useState<string | null>(null);

  const form = useForm<ContactValues>({
    defaultValues: { kind: "email", marketing: false, transactional: true, value: "" },
  });
  const kind = form.watch("kind");

  async function submit(values: ContactValues) {
    setFailure(null);
    try {
      await apiFetch("/v1/account/contacts", {
        body: JSON.stringify({
          kind: values.kind,
          marketing: values.marketing,
          transactional: values.transactional,
          value: normalize(values.kind, values.value),
        }),
        method: "POST",
      });
      form.reset({ kind: values.kind, marketing: false, transactional: true, value: "" });
      onAdded();
      toast.success(translate("contacts.added"));
    } catch (addError) {
      setFailure(describeProblem(addError));
    }
  }

  const flagsInvalid = Boolean(form.formState.errors.transactional);
  return (
    <form
      className="space-y-3 rounded-xl border border-border bg-card p-4"
      noValidate
      onSubmit={form.handleSubmit(submit)}
    >
      <p className="font-medium text-[13.5px]">{translate("contacts.addTitle")}</p>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={kindId}>{translate("contacts.kind")}</Label>
        <Controller
          control={form.control}
          name="kind"
          render={({ field }) => (
            <Select onValueChange={field.onChange} value={field.value}>
              <SelectTrigger id={kindId}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="email">{translate("contacts.kinds.email")}</SelectItem>
                <SelectItem value="phone">{translate("contacts.kinds.phone")}</SelectItem>
                <SelectItem value="telegram">{translate("contacts.kinds.telegram")}</SelectItem>
              </SelectContent>
            </Select>
          )}
        />
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={valueId}>{translate("contacts.value")}</Label>
        <Input
          aria-describedby={`${valueId}-hint`}
          aria-invalid={Boolean(form.formState.errors.value)}
          autoComplete={kind === "email" ? "email" : kind === "phone" ? "tel" : "off"}
          id={valueId}
          inputMode={kind === "phone" ? "tel" : "text"}
          placeholder={translate(`contacts.placeholder.${kind}`)}
          type={kind === "email" ? "email" : "text"}
          {...form.register("value", CONTACT_RULES.value)}
        />
        <p className="text-[12px] text-subtle-foreground leading-relaxed" id={`${valueId}-hint`}>
          {form.formState.errors.value
            ? translate(`contacts.invalid.${kind}`)
            : translate(`contacts.hint.${kind}`)}
        </p>
      </div>

      <fieldset className="space-y-2.5">
        <legend className="sr-only">{translate("contacts.flags")}</legend>
        <FlagRow
          control={form.control}
          description={translate("contacts.transactionalHint")}
          invalid={flagsInvalid}
          label={translate("contacts.transactional")}
          name="transactional"
          rules={CONTACT_RULES.transactional}
        />
        <FlagRow
          control={form.control}
          description={translate("contacts.marketingHint")}
          invalid={flagsInvalid}
          label={translate("contacts.marketing")}
          name="marketing"
        />
        {flagsInvalid && (
          <p className="text-[12px] text-destructive leading-relaxed" role="alert">
            {translate("contacts.flagsRequired")}
          </p>
        )}
      </fieldset>

      {failure && (
        <p className="text-[12.5px] text-destructive leading-relaxed" role="alert">
          {failure}
        </p>
      )}

      <Button className="w-full" disabled={form.formState.isSubmitting} size="lg" type="submit">
        {translate("contacts.add")}
      </Button>
      <p className="text-[12px] text-subtle-foreground leading-relaxed">
        {translate("contacts.verificationHint")}
      </p>
    </form>
  );
}

/** One switch with its label and the sentence explaining what it permits. */
function FlagRow({
  control,
  description,
  invalid,
  label,
  name,
  rules,
}: {
  control: ReturnType<typeof useForm<ContactValues>>["control"];
  description: string;
  invalid: boolean;
  label: string;
  name: "marketing" | "transactional";
  /** Carried through to the controller so the "at least one" rule lives with the field it marks. */
  rules?: { validate: (value: boolean, values: ContactValues) => boolean };
}) {
  const switchId = useId();
  return (
    <div className="flex items-start justify-between gap-3">
      <div className="min-w-0">
        <Label htmlFor={switchId}>{label}</Label>
        <p className="mt-0.5 text-[12px] text-subtle-foreground leading-relaxed">{description}</p>
      </div>
      <Controller
        control={control}
        name={name}
        rules={rules}
        render={({ field }) => (
          <Switch
            aria-invalid={invalid}
            checked={field.value}
            className="mt-1 shrink-0"
            id={switchId}
            onCheckedChange={field.onChange}
          />
        )}
      />
    </div>
  );
}
