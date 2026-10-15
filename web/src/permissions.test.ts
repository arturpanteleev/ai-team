import { describe, expect, it } from 'vitest';
import { mayPerform, permissionRoles } from './permissions';
import type { Permission } from './permissions';
import type { CloudRole } from './types';

// Mirrors the Go table in pkg/cloudidentity (Authorize); change both together.
const expected: Record<Permission, CloudRole[]> = {
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
const allRoles: CloudRole[] = ['product_owner', 'architect', 'developer', 'reviewer', 'qa', 'release_manager', 'admin'];

describe('UI permissions', () => {
  for (const [permission, roles] of Object.entries(expected) as [Permission, CloudRole[]][]) {
    it(`${permission}: only ${roles.join(', ')}`, () => {
      for (const role of allRoles) {
        expect(mayPerform({ actor_id: 'a', roles: [role] }, permission, true)).toBe(roles.includes(role));
      }
    });
  }

  it('the administrator right does not come with the product owner role', () => {
    expect(mayPerform({ actor_id: 'a', roles: ['product_owner'] }, 'gate:override', true)).toBe(false);
    expect(mayPerform({ actor_id: 'a', roles: ['product_owner', 'admin'] }, 'gate:override', true)).toBe(true);
  });

  it('local mode without a principal allows everything, cloud mode without a session nothing', () => {
    expect(mayPerform(null, 'run:start', false)).toBe(true);
    expect(mayPerform(null, 'run:start', true)).toBe(false);
  });

  it('names the roles of a permission for the "no rights" hint', () => {
    expect(permissionRoles['tracker:link']).toBe('Владелец продукта, Архитектор');
  });
});
