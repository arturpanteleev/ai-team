import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { TemplateEditor } from './TemplateEditor';

const api = vi.hoisted(() => ({
  getProjectTemplate: vi.fn(),
  validateProjectTemplate: vi.fn(),
  publishProjectTemplate: vi.fn(),
}));

vi.mock('../api', () => ({
  getActivePrincipal: () => ({ actor_id: 'owner', roles: ['product_owner'] }),
  getProjectTemplate: api.getProjectTemplate,
  validateProjectTemplate: api.validateProjectTemplate,
  publishProjectTemplate: api.publishProjectTemplate,
}));

const firstVersion = 'a'.repeat(64);
const secondVersion = 'b'.repeat(64);
const firstYAML = `schema_version: 5\ntemplate: single-flow\ntitle: One project template\nstages:\n  - id: architect\n    title: Architecture\n    function: architect\n    result: md\n    executor: human\n  - id: analyst\n    title: Analysis\n    function: po\n    result: md\n    executor: human\nreturns:\n  - from: analyst\n    to: architect\n    max_visits: 2\n`;

function templateResponse(version = firstVersion, yaml = firstYAML) {
  return {
    template: {
      valid: true,
      template: 'single-flow',
      title: 'One project template',
      version,
      yaml,
      stages: [
        { id: 'architect', title: 'Architecture', function: 'architect', result: 'md', executor: 'human', max_visits: 2 },
        { id: 'analyst', title: 'Analysis', function: 'po', result: 'md', executor: 'human' },
      ],
      returns: [{ from: 'analyst', to: 'architect', max_visits: 2 }],
    },
    versions: [{ id: version }],
  };
}

beforeEach(() => {
  api.getProjectTemplate.mockReset();
  api.validateProjectTemplate.mockReset();
  api.publishProjectTemplate.mockReset();
  api.getProjectTemplate.mockResolvedValue(templateResponse());
});
afterEach(cleanup);

describe('TemplateEditor', () => {
  it('shows the one selected template as a connected diagram with its return route', async () => {
    render(<TemplateEditor />);

    expect(await screen.findByRole('heading', { name: 'One project template' })).toBeInTheDocument();
    expect(screen.getByRole('list', { name: 'Этапы шаблона по порядку' })).toBeInTheDocument();
    expect(screen.getByText('Architecture')).toBeInTheDocument();
    expect(screen.getByText('Analysis')).toBeInTheDocument();
    expect(screen.getAllByText('architect', { selector: 'code' })).toHaveLength(2);
    expect(screen.getAllByText('analyst', { selector: 'code' })).toHaveLength(2);
    expect(screen.getByText('возвращает в')).toBeInTheDocument();
    expect(screen.getByText(/У проекта один выбранный шаблон/)).toBeInTheDocument();
  });

  it('validates YAML diagnostics and publishes against the version that was loaded', async () => {
    api.validateProjectTemplate
      .mockResolvedValueOnce({ valid: false, diagnostic: 'stages[1]: недостижимый узел' })
      .mockResolvedValueOnce({ valid: true, version: secondVersion });
    api.publishProjectTemplate.mockResolvedValue({ version: secondVersion });
    api.getProjectTemplate.mockResolvedValueOnce(templateResponse()).mockResolvedValueOnce(templateResponse(secondVersion, firstYAML.replace('One project template', 'Published flow')));
    render(<TemplateEditor />);

    const editor = await screen.findByLabelText('Конфигурация выбранного шаблона');
    fireEvent.change(editor, { target: { value: firstYAML + '# draft\n' } });
    fireEvent.click(screen.getByRole('button', { name: 'Проверить YAML' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('недостижимый узел');
    expect(api.publishProjectTemplate).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: 'Проверить и опубликовать версию' }));
    await waitFor(() => expect(api.publishProjectTemplate).toHaveBeenCalledWith(firstYAML + '# draft\n', firstVersion));
    expect(await screen.findByText(/Новая версия опубликована/)).toBeInTheDocument();
    expect(screen.getByText(/текущая/)).toBeInTheDocument();
  });
});
