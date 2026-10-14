import { render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { VersionBadge } from './VersionBadge';

describe('VersionBadge', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    document.querySelectorAll('script').forEach((node) => node.remove());
  });

  it('shows the backend commit and marks local changes', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ backend: { revision: 'abc1234def', modified: true } }), { status: 200 })));
    render(<VersionBadge />);
    expect(await screen.findByText(/API abc1234\*/)).toBeInTheDocument();
    expect(screen.getByRole('status', { name: 'Версии приложения' })).toHaveAttribute('title', expect.stringContaining('локальными правками'));
  });

  it('identifies the UI bundle by its entry script hash and stays usable without the backend', async () => {
    const script = document.createElement('script');
    script.type = 'module';
    script.src = '/assets/index-DEAqc0FF.js';
    document.head.appendChild(script);
    vi.stubGlobal('fetch', vi.fn(async () => { throw new Error('offline'); }));
    render(<VersionBadge />);
    expect(screen.getByText('UI DEAqc0FF')).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText(/API —/)).toBeInTheDocument());
  });
});
