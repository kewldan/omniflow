/**
 * The installation settings sections and the fields each one holds.
 *
 * The schema is declared here rather than as nine hand-written forms because
 * the sections differ in content and not in shape: every one is a document plus
 * an optional set of write-only secrets, saved with a version guard. One
 * renderer over a declaration means a new field is one line, and it means the
 * secret handling cannot be got right in eight places and wrong in the ninth.
 *
 * A field marked `secret` is never populated from the server — the API does not
 * return one — and an empty secret field on save leaves the stored value alone.
 */

export type FieldKind = "text" | "url" | "number" | "boolean" | "textarea" | "secret";

export type SectionField = {
  /** Key inside the section's document, or inside its secrets when `secret`. */
  name: string;
  kind: FieldKind;
  /** Suffix under `admin.installationSettings.fields` for the label and hint. */
  messageKey: string;
};

export type SectionSchema = {
  section: string;
  /** Suffix under `admin.installationSettings.sections`. */
  messageKey: string;
  fields: SectionField[];
};

export const SECTIONS: SectionSchema[] = [
  {
    section: "branding",
    messageKey: "branding",
    fields: [
      { name: "serviceName", kind: "text", messageKey: "serviceName" },
      { name: "supportContact", kind: "text", messageKey: "supportContact" },
      { name: "publicUrl", kind: "url", messageKey: "publicUrl" },
      { name: "termsUrl", kind: "url", messageKey: "termsUrl" },
      { name: "defaultLocale", kind: "text", messageKey: "defaultLocale" },
      { name: "timezone", kind: "text", messageKey: "timezone" },
    ],
  },
  {
    section: "remnawave",
    messageKey: "remnawave",
    fields: [
      { name: "baseUrl", kind: "url", messageKey: "remnawaveBaseUrl" },
      { name: "reconcileIntervalMinutes", kind: "number", messageKey: "reconcileInterval" },
      { name: "compatibilityChecked", kind: "boolean", messageKey: "compatibilityChecked" },
      // The token is write-only. Rotating it means typing a new one; leaving
      // this empty keeps the current one, which is what makes rotation a
      // deliberate act rather than a side effect of editing the base URL.
      { name: "token", kind: "secret", messageKey: "remnawaveToken" },
    ],
  },
  {
    section: "telegram",
    messageKey: "telegram",
    fields: [
      { name: "botUsername", kind: "text", messageKey: "botUsername" },
      { name: "webhookUrl", kind: "url", messageKey: "webhookUrl" },
      { name: "webhookEnabled", kind: "boolean", messageKey: "webhookEnabled" },
      { name: "botToken", kind: "secret", messageKey: "botToken" },
      { name: "webhookSecret", kind: "secret", messageKey: "webhookSecret" },
    ],
  },
  {
    section: "operator_group",
    messageKey: "operatorGroup",
    fields: [
      { name: "chatId", kind: "text", messageKey: "operatorChatId" },
      { name: "createTopics", kind: "boolean", messageKey: "createTopics" },
      { name: "notificationCap", kind: "number", messageKey: "notificationCap" },
      { name: "windowMinutes", kind: "number", messageKey: "windowMinutes" },
    ],
  },
  {
    section: "required_channels",
    messageKey: "requiredChannels",
    fields: [
      { name: "recheckIntervalMinutes", kind: "number", messageKey: "recheckInterval" },
      { name: "graceHours", kind: "number", messageKey: "graceHours" },
      { name: "warnBeforeSuspend", kind: "boolean", messageKey: "warnBeforeSuspend" },
    ],
  },
  {
    section: "maintenance",
    messageKey: "maintenance",
    fields: [
      { name: "autoEnable", kind: "boolean", messageKey: "autoEnable" },
      { name: "failureThreshold", kind: "number", messageKey: "failureThreshold" },
      { name: "noticeEn", kind: "textarea", messageKey: "noticeEn" },
      { name: "noticeRu", kind: "textarea", messageKey: "noticeRu" },
    ],
  },
  {
    section: "notifications",
    messageKey: "notifications",
    fields: [
      { name: "failedPaymentThreshold", kind: "number", messageKey: "failedPaymentThreshold" },
      { name: "driftThreshold", kind: "number", messageKey: "driftThreshold" },
      { name: "outboxBacklogThreshold", kind: "number", messageKey: "outboxThreshold" },
      { name: "quietHoursStart", kind: "text", messageKey: "quietHoursStart" },
      { name: "quietHoursEnd", kind: "text", messageKey: "quietHoursEnd" },
    ],
  },
  {
    section: "telemetry",
    messageKey: "telemetry",
    fields: [
      { name: "enabled", kind: "boolean", messageKey: "telemetryEnabled" },
      { name: "endpoint", kind: "url", messageKey: "telemetryEndpoint" },
    ],
  },
  {
    section: "backup",
    messageKey: "backup",
    fields: [
      { name: "enabled", kind: "boolean", messageKey: "backupEnabled" },
      { name: "intervalHours", kind: "number", messageKey: "backupInterval" },
      { name: "retentionDays", kind: "number", messageKey: "backupRetention" },
      { name: "directory", kind: "text", messageKey: "backupDirectory" },
      { name: "encryptionKey", kind: "secret", messageKey: "backupEncryptionKey" },
    ],
  },
  {
    section: "security",
    messageKey: "security",
    fields: [
      { name: "sessionIdleMinutes", kind: "number", messageKey: "sessionIdle" },
      { name: "sessionAbsoluteHours", kind: "number", messageKey: "sessionAbsolute" },
      { name: "requireTotp", kind: "boolean", messageKey: "requireTotp" },
      { name: "allowedOrigins", kind: "text", messageKey: "allowedOrigins" },
    ],
  },
];
