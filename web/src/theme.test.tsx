import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ThemeProvider, useTheme } from './theme';

function Probe() {
  const { theme, toggleTheme } = useTheme();
  return (
    <>
      <span data-testid="current">{theme}</span>
      <button type="button" onClick={toggleTheme}>toggle</button>
    </>
  );
}

function setMedia(overrides: Record<string, boolean> = {}) {
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    writable: true,
    value: (query: string) => ({
      matches: overrides[query] ?? false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }),
  });
}

function setStorage() {
  const data = new Map<string, string>();
  Object.defineProperty(window, 'localStorage', {
    configurable: true,
    value: {
      getItem: (key: string) => data.get(key) ?? null,
      setItem: (key: string, value: string) => { data.set(key, value); },
      removeItem: (key: string) => { data.delete(key); },
      clear: () => data.clear(),
    },
  });
}

function themeColor(): string | null {
  return document.querySelector('meta[name="theme-color"]')?.getAttribute('content') ?? null;
}

function hasTransitionClass(): boolean {
  return document.documentElement.classList.contains('theme-transition');
}

describe('ThemeProvider', () => {
  beforeEach(() => {
    setMedia();
    setStorage();
    delete document.documentElement.dataset.theme;
    document.documentElement.classList.remove('theme-transition');
    document.head.innerHTML = '<meta name="theme-color" content="#fbf8f3" />';
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it('applies the saved theme and keeps meta theme-color equal to its canvas', () => {
    window.localStorage.setItem('ai-team:theme', 'dark');
    render(<ThemeProvider><Probe /></ThemeProvider>);

    expect(screen.getByTestId('current')).toHaveTextContent('dark');
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark');
    expect(themeColor()).toBe('#120f1c');
    // First paint must not animate from the default theme.
    expect(hasTransitionClass()).toBe(false);
  });

  it('switches themes, updates the meta colour and persists the choice', () => {
    window.localStorage.setItem('ai-team:theme', 'light');
    render(<ThemeProvider><Probe /></ThemeProvider>);
    expect(themeColor()).toBe('#fbf8f3');

    fireEvent.click(screen.getByRole('button', { name: 'toggle' }));

    expect(screen.getByTestId('current')).toHaveTextContent('dark');
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark');
    expect(themeColor()).toBe('#120f1c');
    expect(window.localStorage.getItem('ai-team:theme')).toBe('dark');
  });

  it('adds the smooth-transition class only while the theme changes', () => {
    vi.useFakeTimers();
    window.localStorage.setItem('ai-team:theme', 'light');
    render(<ThemeProvider><Probe /></ThemeProvider>);
    expect(hasTransitionClass()).toBe(false);

    fireEvent.click(screen.getByRole('button', { name: 'toggle' }));
    expect(hasTransitionClass()).toBe(true);

    act(() => { vi.advanceTimersByTime(300); });
    expect(hasTransitionClass()).toBe(false);
  });

  it('skips the transition when reduced motion is preferred', () => {
    vi.useFakeTimers();
    setMedia({ '(prefers-reduced-motion: reduce)': true });
    window.localStorage.setItem('ai-team:theme', 'light');
    render(<ThemeProvider><Probe /></ThemeProvider>);

    fireEvent.click(screen.getByRole('button', { name: 'toggle' }));

    expect(document.documentElement).toHaveAttribute('data-theme', 'dark');
    expect(hasTransitionClass()).toBe(false);
  });
});
