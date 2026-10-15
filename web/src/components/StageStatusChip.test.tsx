import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { StageStatusChip } from './StageStatusChip';
import { Icon } from './Icon';
import { stageStatusLabels, stageStatusTones } from '../stageStatus';
import type { StageStatusKey } from '../types';

afterEach(cleanup);

const statuses = Object.keys(stageStatusLabels) as StageStatusKey[];

describe('StageStatusChip', () => {
  it('shows the same label and an svg icon for every status, never a text glyph', () => {
    for (const status of statuses) {
      const { container, unmount } = render(<StageStatusChip status={status} />);
      expect(screen.getByText(stageStatusLabels[status])).toBeInTheDocument();
      expect(container.querySelector('svg[aria-hidden="true"]')).not.toBeNull();
      expect(container.textContent).toBe(stageStatusLabels[status]);
      unmount();
    }
  });

  it('colours red only for a refusal, an error or a block — amber and yellow belong to recommendations', () => {
    const red = statuses.filter((status) => stageStatusTones[status] === 'danger');
    expect(red.sort()).toEqual(['blocked', 'failed', 'rejected']);
    for (const status of statuses) expect(['gray', 'blue', 'indigo', 'teal', 'green', 'danger']).toContain(stageStatusTones[status]);
  });

  it('falls back to the neutral icon for an unknown status without breaking', () => {
    const { container } = render(<StageStatusChip status={'mystery' as StageStatusKey} />);
    expect(container).toHaveTextContent('mystery');
    expect(container.querySelector('svg')).not.toBeNull();
  });
});

describe('Icon', () => {
  it('is decorative: hidden from assistive technology and not focusable', () => {
    const { container } = render(<Icon name="warning" />);
    const svg = container.querySelector('svg')!;
    expect(svg).toHaveAttribute('aria-hidden', 'true');
    expect(svg).toHaveAttribute('focusable', 'false');
  });
});
