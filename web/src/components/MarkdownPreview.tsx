import ReactMarkdown from 'react-markdown';

// MarkdownPreview renders Markdown the same way everywhere a human reviews a
// result before or after it is stored: the artifact viewer and the stage
// result form. Styling stays with the caller.
export function MarkdownPreview({ markdown, className, label, emptyText }: {
  markdown: string;
  className?: string;
  label?: string;
  emptyText?: string;
}) {
  return <div className={className} aria-label={label}>
    {emptyText !== undefined && !markdown.trim() ? <p>{emptyText}</p> : <ReactMarkdown>{markdown}</ReactMarkdown>}
  </div>;
}
