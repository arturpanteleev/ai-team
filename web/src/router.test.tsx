import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import {
  Link,
  MemoryRouter,
  useLocation,
  useParams,
  useSearchParams,
} from './router';

function RouteProbe() {
  const location = useLocation();
  const params = useParams<{ id?: string; '*'?: string }>();
  const [searchParams, setSearchParams] = useSearchParams();
  return (
    <>
      <span data-testid="path">{location.pathname}</span>
      <span data-testid="id">{params.id || ''}</span>
      <span data-testid="artifact">{params['*'] || ''}</span>
      <span data-testid="view">{searchParams.get('view') || ''}</span>
      <Link to="/artifacts/run-1/reports/final%20report.md?view=raw">
        Открыть артефакт
      </Link>
      <button type="button" onClick={() => setSearchParams({ view: 'rendered' })}>
        Rendered
      </button>
    </>
  );
}

describe('local router', () => {
  it('обрабатывает path params, wildcard, query и client-side navigation', () => {
    render(
      <MemoryRouter initialEntries={['/pipelines/42']}>
        <RouteProbe />
      </MemoryRouter>,
    );

    expect(screen.getByTestId('id')).toHaveTextContent('42');
    fireEvent.click(screen.getByRole('link', { name: 'Открыть артефакт' }));

    expect(screen.getByTestId('path')).toHaveTextContent('/artifacts/run-1/reports/final%20report.md');
    expect(screen.getByTestId('artifact')).toHaveTextContent('run-1/reports/final report.md');
    expect(screen.getByTestId('view')).toHaveTextContent('raw');

    fireEvent.click(screen.getByRole('button', { name: 'Rendered' }));
    expect(screen.getByTestId('view')).toHaveTextContent('rendered');
  });
});
