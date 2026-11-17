/* oxlint-disable react/only-export-components -- provider and hook are one context API */
import { createContext, useContext, useEffect, useMemo, useRef, useState } from 'react';
import type { ReactNode } from 'react';

export type Theme = 'light' | 'dark';

interface ThemeValue {
  theme: Theme;
  toggleTheme: () => void;
}

const ThemeContext = createContext<ThemeValue | null>(null);
const storageKey = 'ai-team:theme';
const themeTransitionClass = 'theme-transition';
/* Должен быть не меньше --duration-theme из index.css. */
const themeTransitionMs = 300;
/* Резерв для окружений без загруженного CSS (например, unit-тестов): --canvas из index.css. */
const canvasFallback: Record<Theme, string> = {
  light: '#fbf8f3',
  dark: '#120f1c',
};

function initialTheme(): Theme {
  try {
    const saved = window.localStorage.getItem(storageKey);
    if (saved === 'light' || saved === 'dark') return saved;
  } catch {
    // Storage can be unavailable in private or restricted browsing contexts.
  }
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function syncThemeColor(theme: Theme) {
  const meta = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]');
  if (!meta) return;
  const canvas = window.getComputedStyle(document.documentElement)
    .getPropertyValue('--canvas')
    .trim();
  meta.content = canvas || canvasFallback[theme];
}

function prefersReducedMotion(): boolean {
  return window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false;
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setTheme] = useState<Theme>(initialTheme);
  const appliedTheme = useRef<Theme | null>(null);
  const transitionTimer = useRef<number | null>(null);

  useEffect(() => {
    const root = document.documentElement;
    const previous = appliedTheme.current;
    appliedTheme.current = theme;
    const animate = previous !== null && previous !== theme && !prefersReducedMotion();

    /* Класс добавляем до смены темы: так браузер видит переход от старых цветов. */
    if (animate) {
      root.classList.add(themeTransitionClass);
      void root.offsetWidth;
    }

    root.dataset.theme = theme;
    syncThemeColor(theme);
    try {
      window.localStorage.setItem(storageKey, theme);
    } catch {
      // The current page still uses the selected theme if persistence is blocked.
    }

    if (animate) {
      if (transitionTimer.current !== null) window.clearTimeout(transitionTimer.current);
      transitionTimer.current = window.setTimeout(() => {
        root.classList.remove(themeTransitionClass);
        transitionTimer.current = null;
      }, themeTransitionMs);
    }
  }, [theme]);

  useEffect(() => () => {
    if (transitionTimer.current !== null) window.clearTimeout(transitionTimer.current);
    document.documentElement.classList.remove(themeTransitionClass);
  }, []);

  const value = useMemo(() => ({
    theme,
    toggleTheme: () => setTheme((current) => current === 'light' ? 'dark' : 'light'),
  }), [theme]);

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme(): ThemeValue {
  const value = useContext(ThemeContext);
  if (!value) throw new Error('useTheme must be used inside ThemeProvider');
  return value;
}
