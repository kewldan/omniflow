"use client";

import { Button } from "@omniflow/ui/button";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Switch } from "@omniflow/ui/switch";
import { toast } from "@omniflow/ui/toast";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { type FormEvent, useId, useState } from "react";
import useSWR, { useSWRConfig } from "swr";
import { useSupportProblem } from "@/components/account/problems";
import { AccountNotice, ListSkeleton } from "@/components/account/state";
import { CountedField } from "@/components/account/support/composer";
import {
  SUPPORT_LIMITS_KEY,
  type SupportConversation,
  type SupportLimits,
} from "@/components/account/support/types";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { useContentKey } from "@/lib/idempotency";

/**
 * Opening a conversation.
 *
 * The limits are loaded before the form is usable rather than alongside it. Both
 * fields count down to the exact figure the API refuses at, and a form that let
 * somebody type 200 characters into a 120-character subject before saying so
 * would be worse than one that waited a moment for the number.
 *
 * Attachments are not offered here: the upload route addresses an existing
 * conversation, so the file goes on the next screen, where it can be attached to
 * a message rather than to a ticket that does not exist yet.
 */
export default function NewSupportTicketPage() {
  const translate = useTranslations("account.support");
  const describeProblem = useSupportProblem();
  const router = useRouter();
  const { mutate: mutateGlobal } = useSWRConfig();
  const {
    data: limits,
    error,
    isLoading,
  } = useSWR<SupportLimits, ApiError>(SUPPORT_LIMITS_KEY, fetcher);
  const idempotencyKey = useContentKey();

  const [subject, setSubject] = useState("");
  const [message, setMessage] = useState("");
  const [touched, setTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  // What the ticket is about, and whether it is urgent: a payment problem goes
  // to its own queue, and urgent raises the priority the desk sorts by.
  const [topic, setTopic] = useState("general");
  const [urgent, setUrgent] = useState(false);
  const topicId = useId();
  const urgentId = useId();

  const subjectInvalid = touched && !subject.trim();
  const messageInvalid = touched && !message.trim();

  async function submit(event: FormEvent) {
    event.preventDefault();
    setTouched(true);
    if (!subject.trim() || !message.trim()) {
      return;
    }
    setBusy(true);
    try {
      const conversation = await apiFetch<SupportConversation>("/v1/account/support/tickets", {
        body: JSON.stringify({
          message: message.trim(),
          subject: subject.trim(),
          topic: topic === "general" ? "" : topic,
          urgent,
        }),
        headers: {
          // A separator no subject can contain, so "a b"+"c" and "a"+"b c" differ.
          "Idempotency-Key": idempotencyKey(`${subject.trim()}\u0000${message.trim()}`),
        },
        method: "POST",
      });
      // The list is dropped rather than patched. The server decides ordering,
      // status, and the unread count, and splicing a locally-built row in would
      // be this panel inventing a ticket that does not match the one stored.
      await mutateGlobal(
        (key) => typeof key === "string" && key.startsWith("/v1/account/support/tickets?"),
      );
      toast.success(translate("new.created"));
      router.replace(`/account/support/${conversation.ticket.id}`);
    } catch (createError) {
      toast.error(describeProblem(createError));
      setBusy(false);
    }
  }

  if (isLoading) {
    return <ListSkeleton rows={2} />;
  }
  if (error || !limits) {
    return (
      <AccountNotice
        description={translate("tickets.errorDescription")}
        title={translate("tickets.error")}
        variant={error?.status === 503 ? "offline" : "danger"}
      />
    );
  }

  return (
    <form className="animate-step-in space-y-5" noValidate onSubmit={submit}>
      <header className="space-y-1">
        <h1 className="font-semibold text-[19px] tracking-[-0.02em]">{translate("new.title")}</h1>
        <p className="text-[12.5px] text-muted-foreground leading-relaxed">
          {translate("new.description")}
        </p>
      </header>

      <div className="space-y-1.5">
        <Label htmlFor={topicId}>{translate("new.topic")}</Label>
        <Select disabled={busy} onValueChange={setTopic} value={topic}>
          <SelectTrigger id={topicId}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="general">{translate("new.topics.general")}</SelectItem>
            <SelectItem value="payment">{translate("new.topics.payment")}</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div className="flex items-center gap-2">
        <Switch checked={urgent} disabled={busy} id={urgentId} onCheckedChange={setUrgent} />
        <Label htmlFor={urgentId}>{translate("new.urgent")}</Label>
      </div>

      <CountedField
        disabled={busy}
        invalid={subjectInvalid ? translate("new.subjectRequired") : undefined}
        label={translate("new.subject")}
        maxLength={limits.maxSubjectLength}
        onChange={setSubject}
        placeholder={translate("new.subjectPlaceholder")}
        value={subject}
      />

      <CountedField
        disabled={busy}
        invalid={messageInvalid ? translate("new.messageRequired") : undefined}
        label={translate("new.message")}
        maxLength={limits.maxMessageLength}
        multiline
        onChange={setMessage}
        placeholder={translate("new.messagePlaceholder")}
        value={message}
      />

      <Button className="w-full" disabled={busy} size="lg" type="submit">
        {busy ? translate("new.submitting") : translate("new.submit")}
      </Button>
    </form>
  );
}
