import type { StageStatusKey } from '../types';
import { stageStatusGlyphs, stageStatusLabels, stageStatusTones } from '../stageStatus';
import styles from './StageStatusChip.module.css';

// Статус этапа, вычисленный сервером. Подпись и значок одинаковы на карточке
// задачи, в маршруте и на таймлайне; цвет только дублирует их.
export function StageStatusChip({ status, title }: { status: StageStatusKey; title?: string }) {
  return (
    <span className={styles.chip} data-status={status} data-tone={stageStatusTones[status] ?? 'gray'} title={title}>
      <span className={styles.glyph} aria-hidden="true">{stageStatusGlyphs[status] ?? '○'}</span>
      {stageStatusLabels[status] ?? status}
    </span>
  );
}
