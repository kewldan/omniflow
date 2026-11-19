/**
 * File types as a customer knows them: "PNG, JPEG, PDF", not
 * "image/png, image/jpeg, application/pdf".
 *
 * The support upload printed the API's allowlist verbatim. A MIME type is a
 * protocol detail; the person attaching a screenshot thinks in the extension
 * their phone shows them.
 */
const NAMES: Record<string, string> = {
  "application/pdf": "PDF",
  "application/zip": "ZIP",
  "image/gif": "GIF",
  "image/heic": "HEIC",
  "image/jpeg": "JPEG",
  "image/png": "PNG",
  "image/webp": "WebP",
  "text/csv": "CSV",
  "text/plain": "TXT",
  "video/mp4": "MP4",
};

/** One media type's familiar name; an unknown one is its subtype, upper-cased. */
export function describeMediaType(mediaType: string): string {
  const normalized = mediaType.trim().toLowerCase().split(";")[0] ?? "";
  if (normalized === "") {
    return "?";
  }
  const known = NAMES[normalized];
  if (known) {
    return known;
  }
  const subtype = normalized.split("/")[1] ?? normalized;
  return subtype.replace(/^x-/, "").toUpperCase();
}

/** A list of media types as a comma-separated list of names, without repeats. */
export function describeMediaTypes(mediaTypes: readonly string[]): string {
  return [...new Set(mediaTypes.map(describeMediaType))].join(", ");
}
