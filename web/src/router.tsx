/* oxlint-disable react/only-export-components -- routing adapter intentionally exports providers, links and hooks */
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from 'react';
import type {
  AnchorHTMLAttributes,
  MouseEvent,
  ReactNode,
} from 'react';

interface RouterLocation {
  pathname: string;
  search: string;
}

type NavigateTarget = string | number;
type Navigate = (target: NavigateTarget) => void;

interface RouterContextValue {
  location: RouterLocation;
  navigate: Navigate;
}

const RouterContext = createContext<RouterContextValue | null>(null);

function currentBrowserLocation(): RouterLocation {
  return { pathname: window.location.pathname, search: window.location.search };
}

function useRouterContext(): RouterContextValue {
  const value = useContext(RouterContext);
  if (!value) {
    throw new Error('router hooks must be used inside BrowserRouter or MemoryRouter');
  }
  return value;
}

export function BrowserRouter({ children }: { children: ReactNode }) {
  const [location, setLocation] = useState(currentBrowserLocation);

  useEffect(() => {
    const onPopState = () => setLocation(currentBrowserLocation());
    window.addEventListener('popstate', onPopState);
    return () => window.removeEventListener('popstate', onPopState);
  }, []);

  const navigate = useCallback<Navigate>((target) => {
    if (typeof target === 'number') {
      window.history.go(target);
      return;
    }
    window.history.pushState(null, '', target);
    setLocation(currentBrowserLocation());
  }, []);

  const value = useMemo(() => ({ location, navigate }), [location, navigate]);
  return <RouterContext.Provider value={value}>{children}</RouterContext.Provider>;
}

export function MemoryRouter({
  children,
  initialEntries = ['/'],
}: {
  children: ReactNode;
  initialEntries?: string[];
}) {
  const initial = new URL(initialEntries[0] ?? '/', 'http://localhost');
  const [location, setLocation] = useState<RouterLocation>({
    pathname: initial.pathname,
    search: initial.search,
  });

  const navigate = useCallback<Navigate>((target) => {
    if (typeof target === 'number') {
      return;
    }
    const next = new URL(target, 'http://localhost');
    setLocation({ pathname: next.pathname, search: next.search });
  }, []);

  const value = useMemo(() => ({ location, navigate }), [location, navigate]);
  return <RouterContext.Provider value={value}>{children}</RouterContext.Provider>;
}

interface LinkProps extends Omit<AnchorHTMLAttributes<HTMLAnchorElement>, 'href'> {
  to: string;
}

export function Link({ to, onClick, ...props }: LinkProps) {
  const { navigate } = useRouterContext();
  const handleClick = (event: MouseEvent<HTMLAnchorElement>) => {
    onClick?.(event);
    if (
      event.defaultPrevented ||
      event.button !== 0 ||
      event.metaKey ||
      event.ctrlKey ||
      event.shiftKey ||
      event.altKey
    ) {
      return;
    }
    event.preventDefault();
    navigate(to);
  };
  return <a {...props} href={to} onClick={handleClick} />;
}

interface NavLinkProps extends Omit<LinkProps, 'className'> {
  end?: boolean;
  className?: string | ((state: { isActive: boolean }) => string);
}

export function NavLink({ to, end = false, className, ...props }: NavLinkProps) {
  const { location } = useRouterContext();
  const isActive = end
    ? location.pathname === to
    : location.pathname === to || location.pathname.startsWith(`${to}/`);
  const resolvedClassName = typeof className === 'function'
    ? className({ isActive })
    : className;
  return <Link {...props} to={to} className={resolvedClassName} />;
}

export function useNavigate(): Navigate {
  return useRouterContext().navigate;
}

export function useLocation(): RouterLocation {
  return useRouterContext().location;
}

export function useParams<T extends Record<string, string | undefined>>(): T {
  const { pathname } = useRouterContext().location;
  const values: Record<string, string> = {};
  const pipeline = pathname.match(/^\/pipelines\/([^/]+)$/);
  const artifact = pathname.match(/^\/artifacts\/(.+)$/);
  if (pipeline) {
    values.id = decodeURIComponent(pipeline[1]);
  }
  if (artifact) {
    values['*'] = artifact[1].split('/').map(decodeURIComponent).join('/');
  }
  return values as T;
}

type SearchParamsInput = URLSearchParams | Record<string, string>;

export function useSearchParams(): [
  URLSearchParams,
  (next: SearchParamsInput) => void,
] {
  const { location, navigate } = useRouterContext();
  const params = useMemo(() => new URLSearchParams(location.search), [location.search]);
  const setParams = useCallback((next: SearchParamsInput) => {
    const updated = next instanceof URLSearchParams
      ? next
      : new URLSearchParams(next);
    const query = updated.toString();
    navigate(`${location.pathname}${query ? `?${query}` : ''}`);
  }, [location.pathname, navigate]);
  return [params, setParams];
}
