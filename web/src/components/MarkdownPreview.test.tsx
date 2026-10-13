import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { MarkdownPreview } from './MarkdownPreview';

afterEach(cleanup);

describe('MarkdownPreview', () => {
  it('renders markdown inside the caller-styled container', () => {
    const { container } = render(<MarkdownPreview className="viewer" markdown={'# Итог\n\nтекст'} />);
    expect(screen.getByRole('heading', { name: 'Итог', level: 1 })).toBeInTheDocument();
    expect(container.firstChild).toHaveClass('viewer');
  });

  it('shows the empty text only when the caller asks for it', () => {
    render(<MarkdownPreview label="Предпросмотр" markdown="  " emptyText="Пока пусто." />);
    expect(screen.getByLabelText('Предпросмотр')).toHaveTextContent('Пока пусто.');
    cleanup();
    const { container } = render(<MarkdownPreview markdown="" />);
    expect(container.firstChild).toBeEmptyDOMElement();
  });
});
