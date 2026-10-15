import { useCallback, useEffect, useState } from 'react';
import { getProjects } from '../api';
import type { Project } from '../types';

export interface ProjectsState {
  /** Все проекты, включая архивные: фильтры читают историю архивных. */
  projects: Project[];
  /** Проекты, в которые можно создавать задачи. */
  active: Project[];
  loaded: boolean;
  error: string;
  reload: () => Promise<void>;
}

export function projectLabel(project: Project): string {
  return project.archived ? `${project.name} (архив)` : project.name;
}

export function useProjects(): ProjectsState {
  const [projects, setProjects] = useState<Project[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState('');

  const reload = useCallback(async () => {
    try {
      const response = await getProjects(true);
      setProjects(response.projects ?? []);
      setError('');
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Не удалось загрузить проекты.');
    } finally {
      setLoaded(true);
    }
  }, []);

  useEffect(() => {
    void reload();
  }, [reload]);

  return { projects, active: projects.filter((project) => !project.archived), loaded, error, reload };
}
