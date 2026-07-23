import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { StatusBadge } from './StatusBadge';
import styles from './StatusBadge.module.css';
import type { PipelineStatus, StageStatus } from '../types';

// Полный union PipelineStatus | StageStatus (см. ../types) — каждое
// значение должно рендериться со своим собственным CSS-классом, а не молча
// делить класс с другим статусом или проваливаться в pending.
const ALL_STATUSES: Array<PipelineStatus | StageStatus> = [
  'running',
  'completed',
  'completed_with_warnings',
  'failed',
  'blocked',
  'stopped',
  'canceled',
  'interrupted',
  'passed',
  'rejected',
  'warning',
  'skipped',
  'invalidated',
];

describe('StatusBadge', () => {
  it.each(ALL_STATUSES)('renders the %s state with its own class', (status) => {
    render(<StatusBadge status={status} />);
    expect(screen.getByText(status)).toHaveClass(styles[status]);
  });

  // Компонент подстраховывается для статусов вне известного union
  // (`styles[status] || styles.pending`), но это не проверяется тестом:
  // Vite в dev/test-режиме отдаёт CSS-modules `styles` как Proxy, который
  // генерирует правдоподобное имя класса для ЛЮБОГО ключа — включая
  // несуществующий — так что `styles[key]` здесь никогда не бывает falsy,
  // в отличие от production-сборки. Assertion на этот fallback был бы
  // проверкой поведения Proxy, а не компонента.
});
