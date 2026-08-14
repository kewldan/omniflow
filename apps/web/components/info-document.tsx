import type { ReactNode } from "react";

/**
 * An operator's published document, rendered as text nodes and nothing else.
 *
 * The API parses the operator's source into typed blocks and typed inline runs —
 * see `internal/infopage` — so this component never receives markup and never
 * produces any. There is no `dangerouslySetInnerHTML` here and no sanitiser,
 * because there is nothing to sanitise: React escapes every string it renders,
 * and the only element this creates from operator input is an anchor whose
 * target the API has already checked is an https address.
 *
 * That is the whole reason the format is a block tree rather than Markdown. A
 * terms page is written by an operator and served from the origin that holds
 * the session cookie; rendering it as HTML would put the page one sanitiser bug
 * away from stored cross-site scripting.
 */

export type Span = { text: string; href?: string };

export type Block = {
  kind: "heading" | "paragraph" | "list";
  spans?: Span[];
  items?: Span[][];
  ordered?: boolean;
};

export type InfoDocument = { blocks: Block[] };

export function InfoDocumentView({ document }: { document: InfoDocument }) {
  return (
    <div className="space-y-4">
      {document.blocks.map((block, index) => (
        // Blocks have no identity of their own — they are positions in one
        // document that is replaced wholesale on every edit — so the index is
        // the honest key rather than a fabricated identifier.
        // biome-ignore lint/suspicious/noArrayIndexKey: a parsed document has no stable per-block identity
        <BlockView block={block} key={index} />
      ))}
    </div>
  );
}

function BlockView({ block }: { block: Block }): ReactNode {
  if (block.kind === "heading") {
    return (
      <h2 className="pt-2 font-semibold text-[15px] tracking-tight">
        <Spans spans={block.spans ?? []} />
      </h2>
    );
  }

  if (block.kind === "list") {
    const items = block.items ?? [];
    const className = "space-y-1.5 pl-5 text-[13.5px] leading-relaxed";
    return block.ordered ? (
      <ol className={`list-decimal ${className}`}>
        {items.map((item, index) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: list items are positions, not records
          <li key={index}>
            <Spans spans={item} />
          </li>
        ))}
      </ol>
    ) : (
      <ul className={`list-disc ${className}`}>
        {items.map((item, index) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: list items are positions, not records
          <li key={index}>
            <Spans spans={item} />
          </li>
        ))}
      </ul>
    );
  }

  return (
    <p className="text-[13.5px] leading-relaxed">
      <Spans spans={block.spans ?? []} />
    </p>
  );
}

function Spans({ spans }: { spans: Span[] }) {
  return (
    <>
      {spans.map((span, index) =>
        span.href ? (
          // The target was validated as https by the API before it ever reached
          // a span. `noopener` because the destination is somebody else's site.
          <a
            className="underline underline-offset-2"
            href={span.href}
            // biome-ignore lint/suspicious/noArrayIndexKey: spans are positions within a line
            key={index}
            rel="noreferrer noopener"
            target="_blank"
          >
            {span.text}
          </a>
        ) : (
          // biome-ignore lint/suspicious/noArrayIndexKey: spans are positions within a line
          <span key={index}>{span.text}</span>
        ),
      )}
    </>
  );
}
