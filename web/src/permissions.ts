// Права интерфейса. Зеркало cloudidentity.Authorize (pkg/cloudidentity/identity.go):
// клиент только решает, показывать ли действие, окончательный отказ всегда за
// сервером. В локальном режиме (principal нет, облачной аутентификации нет)
// доступно всё; в облачном режиме без сессии — ничего.

import { getActivePrincipal, isAuthenticationRequired } from './api';
import type { CloudRole, Principal } from './types';

export type Permission =
  | 'run:start' | 'run:resume' | 'run:cancel' | 'run:deliver' | 'artifact:edit' | 'template:edit'
  | 'team:manage' | 'project:manage' | 'tracker:link' | 'gate:override';

const grants: Record<Permission, CloudRole[]> = {
  'run:start': ['product_owner', 'architect'],
  'run:resume': ['product_owner', 'architect', 'developer', 'qa'],
  'run:cancel': ['product_owner', 'release_manager'],
  'run:deliver': ['product_owner', 'release_manager'],
  'artifact:edit': ['product_owner', 'architect', 'reviewer', 'qa'],
  'template:edit': ['product_owner', 'architect'],
  'team:manage': ['product_owner'],
  'project:manage': ['product_owner', 'architect'],
  'tracker:link': ['product_owner', 'architect'],
  'gate:override': ['admin'],
};

function cloudMode(): boolean {
  try {
    return isAuthenticationRequired();
  } catch {
    return false;
  }
}

export function mayPerform(principal: Principal | null, permission: Permission, cloud = cloudMode()): boolean {
  if (!principal) return !cloud;
  return grants[permission].some((role) => principal.roles.includes(role));
}

export function can(permission: Permission): boolean {
  return mayPerform(getActivePrincipal(), permission);
}

export const roleLabels: Record<string, string> = {
  product_owner: 'Владелец продукта',
  architect: 'Архитектор',
  developer: 'Разработчик',
  reviewer: 'Ревьюер',
  qa: 'Тестировщик',
  release_manager: 'Менеджер выпуска',
  // Системное право, а не функция процесса: только оно пропускает задачу после проваленного обязательного гейта.
  admin: 'Администратор',
};

export const permissionRoles: Record<Permission, string> = Object.fromEntries(
  (Object.keys(grants) as Permission[]).map((key) => [key, grants[key].map((role) => roleLabels[role]).join(', ')]),
) as Record<Permission, string>;
