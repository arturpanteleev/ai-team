import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { NewTask } from './NewTask';
import { createTask, getTemplatesList } from '../api';

const session = vi.hoisted(() => ({ principal: { actor_id: 'product-1', roles: ['product_owner'] } }));

vi.mock('../api', () => ({
  createTask: vi.fn(),
  getActivePrincipal: () => session.principal,
  getTemplatesList: vi.fn(),
}));

const templatesResponse = {
  templates: [{
    valid: true,
    template: 'standard',
    title: 'Стандартный маршрут',
    version: 'abcdef1234567890',
    stages: [],
  }],
  active_template: 'standard',
  versions: [],
};

beforeEach(() => {
  session.principal = { actor_id: 'product-1', roles: ['product_owner'] };
  vi.mocked(getTemplatesList).mockResolvedValue(templatesResponse);
  vi.mocked(createTask).mockResolvedValue({ run_id: 'run-new-1' });
});

afterEach(cleanup);

describe('New task screen', () => {
  it('создаёт задачу с шаблоном, режимом, идеей и внешними ссылками', async () => {
    render(<NewTask />);
    await screen.findByLabelText('Шаблон');

    fireEvent.change(screen.getByLabelText('Название'), { target: { value: 'Онбординг' } });
    fireEvent.change(screen.getByLabelText('Текст идеи'), { target: { value: 'Сократить путь до первой ценности' } });
    fireEvent.change(screen.getByLabelText('Внешние ссылки'), {
      target: { value: 'JIRA-42 https://tracker.example/JIRA-42\nhttps://design.example/board' },
    });
    fireEvent.click(screen.getByRole('radio', { name: /Авто/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Создать задачу' }));

    await waitFor(() => expect(createTask).toHaveBeenCalledWith(
      expect.objectContaining({
        feature: 'Онбординг',
        idea: 'Сократить путь до первой ценности',
        template: 'standard',
        mode: 'auto',
        external_links: ['JIRA-42 https://tracker.example/JIRA-42', 'https://design.example/board'],
      }),
      expect.stringMatching(/^create-task-/),
    ));
    expect(await screen.findByText(/Задача создана: run-new-1/)).toBeInTheDocument();
    expect(screen.getByLabelText('Название')).toHaveValue('');
  });

  it('показывает ошибку сервера и сохраняет введённое', async () => {
    vi.mocked(createTask).mockRejectedValueOnce(new Error('auto mode не поддерживается этим режимом запуска'));
    render(<NewTask />);
    await screen.findByLabelText('Шаблон');

    fireEvent.change(screen.getByLabelText('Название'), { target: { value: 'Онбординг' } });
    fireEvent.change(screen.getByLabelText('Текст идеи'), { target: { value: 'Идея' } });
    fireEvent.click(screen.getByRole('button', { name: 'Создать задачу' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('auto mode не поддерживается этим режимом запуска');
    expect(screen.getByLabelText('Название')).toHaveValue('Онбординг');
  });

  it('не показывает форму участникам без роли Product Owner', () => {
    session.principal = { actor_id: 'dev-1', roles: ['developer'] };
    render(<NewTask />);

    expect(screen.getByText('Создавать задачи может Product Owner.')).toBeInTheDocument();
    expect(screen.queryByLabelText('Название')).not.toBeInTheDocument();
  });
});
