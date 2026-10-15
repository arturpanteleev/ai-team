import type { StageStatusKey } from '../types';
import { stageStatusLabels, stageStatusTones } from '../stageStatus';
import { Icon } from './Icon';
import styles from './StageStatusChip.module.css';

// Статус этапа, вычисленный сервером. Подпись и значок одинаковы на карточке
// задачи, в маршруте и на таймлайне; цвет только дублирует их.
export function StageStatusChip({ status, title }: { status: StageStatusKey; title?: string }) {
  return (
    <span className={styles.chip} data-status={status} data-tone={stageStatusTones[status] ?? 'gray'} title={title}>
      <Icon name={status in stageStatusLabels ? status : 'not_reached'} size={12} className={styles.glyph} />
      {stageStatusLabels[status] ?? status}
    </span>
  );
}
