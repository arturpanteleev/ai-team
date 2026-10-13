import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { NewTask } from './NewTask';
import { MemoryRouter } from '../router';
import { createTask, getTemplatesList } from '../api';

const session = vi.hoisted(() => ({
  principal: { actor_id: 'product-1', roles: ['product_owner'] } as { actor_id: string; roles: string[] } | null,
  authenticationRequired: true,
}));

vi.mock('../api', () => ({
  createTask: vi.fn(),
  getActivePrincipal: () => session.principal,
  isAuthenticationRequired: () => session.authenticationRequired,
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
  vi.mocked(createTask).mockClear();
  session.principal = { actor_id: 'product-1', roles: ['product_owner'] };
  session.authenticationRequired = true;
  vi.mocked(getTemplatesList).mockResolvedValue(templatesResponse);
  vi.mocked(createTask).mockResolvedValue({ run_id: 'run-new-1' });
});

afterEach(cleanup);

describe('New task screen', () => {
  it('создаёт задачу с шаблоном, режимом, идеей и внешними ссылками', async () => {
    render(<MemoryRouter><NewTask /></MemoryRouter>);
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
        feature: 'onbording',
        title: 'Онбординг',
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
    render(<MemoryRouter><NewTask /></MemoryRouter>);
    await screen.findByLabelText('Шаблон');

    fireEvent.change(screen.getByLabelText('Название'), { target: { value: 'Онбординг' } });
    fireEvent.change(screen.getByLabelText('Текст идеи'), { target: { value: 'Идея' } });
    fireEvent.click(screen.getByRole('button', { name: 'Создать задачу' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('auto mode не поддерживается этим режимом запуска');
    expect(screen.getByLabelText('Название')).toHaveValue('Онбординг');
  });

  it('не отправляет задачу с недопустимым идентификатором и называет неверный символ', async () => {
    render(<MemoryRouter><NewTask /></MemoryRouter>);
    await screen.findByLabelText('Шаблон');

    fireEvent.change(screen.getByLabelText('Название'), { target: { value: 'Вход' } });
    fireEvent.change(screen.getByLabelText('Идентификатор'), { target: { value: 'вход' } });
    fireEvent.change(screen.getByLabelText('Текст идеи'), { target: { value: 'Идея' } });
    fireEvent.click(screen.getByRole('button', { name: 'Создать задачу' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('Первый символ должен быть латинской буквой или цифрой, а не «в».');
    expect(screen.getByLabelText('Идентификатор')).toHaveAttribute('aria-invalid', 'true');
    expect(createTask).not.toHaveBeenCalled();
  });

  it('показывает ошибку сервера по feature у поля идентификатора', async () => {
    vi.mocked(createTask).mockRejectedValueOnce(new Error('feature и непустой task обязательны'));
    render(<MemoryRouter><NewTask /></MemoryRouter>);
    await screen.findByLabelText('Шаблон');

    fireEvent.change(screen.getByLabelText('Название'), { target: { value: 'Вход' } });
    fireEvent.change(screen.getByLabelText('Текст идеи'), { target: { value: 'Идея' } });
    fireEvent.click(screen.getByRole('button', { name: 'Создать задачу' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('feature и непустой task обязательны');
    expect(screen.getByLabelText('Идентификатор')).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getByLabelText('Идентификатор')).toHaveValue('vkhod');
  });

  it('не показывает форму участникам без роли Product Owner', () => {
    session.principal = { actor_id: 'dev-1', roles: ['developer'] };
    render(<MemoryRouter><NewTask /></MemoryRouter>);

    expect(screen.getByText('Создавать задачи может Product Owner.')).toBeInTheDocument();
    expect(screen.queryByLabelText('Название')).not.toBeInTheDocument();
  });

  it('в облачном режиме без principal не открывает форму создания', () => {
    session.principal = null;
    session.authenticationRequired = true;
    render(<MemoryRouter><NewTask /></MemoryRouter>);

    expect(screen.getByText('Войдите, чтобы создавать задачи.')).toBeInTheDocument();
    expect(screen.queryByLabelText('Название')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Создать задачу' })).not.toBeInTheDocument();
  });

  it('в локальном режиме без principal форма доступна', async () => {
    session.principal = null;
    session.authenticationRequired = false;
    render(<MemoryRouter><NewTask /></MemoryRouter>);

    expect(await screen.findByLabelText('Название')).toBeInTheDocument();
    expect(screen.queryByText('Войдите, чтобы создавать задачи.')).not.toBeInTheDocument();
  });
});
