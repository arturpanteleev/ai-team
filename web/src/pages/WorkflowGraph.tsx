import { useEffect, useId, useMemo, useState } from 'react';
import { Link, useParams } from '../router';
import { getGraph, getRunGraph } from '../api';
import { usePageTitle } from '../hooks/usePageTitle';
import type { GraphView, StageStatusEntry, WorkflowNode } from '../types';
import styles from './WorkflowGraph.module.css';

const NODE_W = 208;
const NODE_H = 64;
const GAP = 72;
const PAD_X = 20;
const PAD_Y = 24;
const RETURN_LABEL_X = 244;

const TERMINALS: Record<string, string> = {
  $complete: 'Завершено',
  $failed: 'Ошибка',
  $blocked: 'Заблокировано',
  $stop: 'Остановлено',
};

type Position = { x: number; y: number; centerY: number };
type RenderNode = {
  key: string;
  label: string;
  sub: string;
  pos: Position;
  terminal: boolean;
  status?: StageStatusEntry;
  current: boolean;
};
type RenderEdge = {
  id: string;
  path: string;
  label: string;
  labelX: number;
  labelY: number;
  backward: boolean;
  active: boolean;
};

function nodeKey(node: WorkflowNode): string {
  return node.id ?? node.name ?? '';
}

function isTerminal(key: string): boolean {
  return Object.prototype.hasOwnProperty.call(TERMINALS, key);
}

function labelFor(key: string): string {
  return TERMINALS[key] ?? key;
}

function nodeSub(node: WorkflowNode | undefined, status: StageStatusEntry | undefined, mode: GraphView['mode']): string {
  if (mode === 'run' && status && status.visit_count > 0) {
    return `Визит ${status.active_visit} из ${status.visit_count}`;
  }
  if (node?.max_visits) return `Лимит: ${node.max_visits}`;
  return '';
}

function statusClass(status: StageStatusEntry | undefined): string {
  if (!status?.visited) return '';
  if (['passed', 'skipped'].includes(status.status)) return styles.nodePassed;
  if (['failed', 'rejected', 'blocked', 'canceled'].includes(status.status)) return styles.nodeFailed;
  return styles.nodeVisited;
}

function buildLayout(view: GraphView): {
  nodes: RenderNode[];
  edges: RenderEdge[];
  width: number;
  height: number;
} {
  const declared = view.nodes ?? [];
  const statuses = new Map((view.stage_statuses?.stages ?? []).map((entry) => [entry.stage_id, entry]));
  const currentKey = view.current_stage || view.stage_statuses?.current_stage || '';
  const terminalTargets = new Set<string>();
  for (const edge of view.edges ?? []) if (isTerminal(edge.to)) terminalTargets.add(edge.to);
  const keys = [...declared.map(nodeKey).filter(Boolean), ...[...terminalTargets].filter((key) => !declared.some((node) => nodeKey(node) === key))];
  const order = new Map(keys.map((key, index) => [key, index]));
  const positions = new Map<string, Position>();
  keys.forEach((key, index) => {
    const y = PAD_Y + index * (NODE_H + GAP);
    positions.set(key, { x: PAD_X, y, centerY: y + NODE_H / 2 });
  });

  const nodes: RenderNode[] = keys.map((key) => {
    const terminal = isTerminal(key);
    const node = declared.find((item) => nodeKey(item) === key);
    const status = statuses.get(key);
    return {
      key,
      label: terminal ? labelFor(key) : (view.mode === 'run' && status?.title) || labelFor(key),
      sub: terminal ? '' : nodeSub(node, status, view.mode),
      pos: positions.get(key)!,
      terminal,
      status,
      current: view.mode === 'run' && !terminal && key === currentKey,
    };
  });

  const edges: RenderEdge[] = [];
  const arcTotal = (view.edges ?? []).filter((edge) => positions.has(edge.from) && positions.has(edge.to)
    && (order.get(edge.to) ?? 0) <= (order.get(edge.from) ?? 0)).length
    + (view.returns ?? []).filter((route) => positions.has(route.from) && positions.has(route.to)).length;
  const forwardCount = new Map<string, number>();
  for (const edge of view.edges ?? []) forwardCount.set(edge.from, (forwardCount.get(edge.from) ?? 0) + 1);
  const forwardSeen = new Map<string, number>();
  const returnLanes = new Map<string, number>();
  let laneCount = 0;
  const laneFor = (key: string) => {
    if (!returnLanes.has(key)) {
      returnLanes.set(key, laneCount);
      laneCount += 1;
    }
    return returnLanes.get(key)!;
  };

  const addArc = (id: string, fromKey: string, toKey: string, label: string, backward: boolean, active: boolean) => {
    const from = positions.get(fromKey);
    const to = positions.get(toKey);
    if (!from || !to) return;
    const lane = laneFor(id);
    const bulgeX = 340 + 26 * lane;
    const laneShift = (lane - (arcTotal - 1) / 2) * 16;
    const labelY = (from.centerY + to.centerY) / 2 + laneShift;
    const startX = from.x + NODE_W;
    edges.push({
      id,
      path: `M ${startX} ${from.centerY} C ${bulgeX} ${from.centerY}, ${bulgeX} ${to.centerY}, ${startX} ${to.centerY}`,
      label,
      labelX: RETURN_LABEL_X,
      labelY,
      backward,
      active,
    });
  };

  for (const edge of view.edges ?? []) {
    const from = positions.get(edge.from);
    const to = positions.get(edge.to);
    if (!from || !to) continue;
    const active = view.mode === 'run' && edge.from === currentKey;
    if ((order.get(edge.to) ?? 0) <= (order.get(edge.from) ?? 0)) {
      addArc(`edge:${edge.from}:${edge.outcome}:${edge.to}`, edge.from, edge.to, edge.outcome, true, active);
      continue;
    }
    const count = forwardCount.get(edge.from) ?? 1;
    const seen = forwardSeen.get(edge.from) ?? 0;
    forwardSeen.set(edge.from, seen + 1);
    const offset = (seen - (count - 1) / 2) * 22;
    const x1 = from.x + NODE_W / 2 + offset;
    const x2 = to.x + NODE_W / 2 + offset;
    const y1 = from.y + NODE_H;
    const y2 = to.y;
    edges.push({
      id: `edge:${edge.from}:${edge.outcome}:${edge.to}`,
      path: `M ${x1} ${y1} C ${x1} ${y1 + 28}, ${x2} ${y2 - 28}, ${x2} ${y2}`,
      label: edge.outcome,
      labelX: (x1 + x2) / 2 + 8,
      labelY: (y1 + y2) / 2,
      backward: false,
      active,
    });
  }

  for (const route of view.returns ?? []) {
    const label = `return_to_${route.to}${route.max_visits ? ` · макс. ${route.max_visits}` : ''}`;
    addArc(`return:${route.from}:${route.to}`, route.from, route.to, label, false,
      view.mode === 'run' && route.from === currentKey);
  }

  const labelWidth = Math.max(0, ...edges.map((edge) => RETURN_LABEL_X + edge.label.length * 6 + 16));
  const width = Math.max(450, labelWidth, 430 + 26 * laneCount);
  const height = PAD_Y * 2 + keys.length * NODE_H + Math.max(0, keys.length - 1) * GAP;
  return { nodes, edges, width, height };
}

export function WorkflowGraph() {
  const { graphID, runID } = useParams<{ graphID?: string; runID?: string }>();
  const [view, setView] = useState<GraphView | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const markerPrefix = useId().replace(/:/g, '');

  usePageTitle(view?.title ?? (runID ? 'Граф прогона' : 'Граф workflow'));

  useEffect(() => {
    let active = true;
    setView(null);
    setLoading(true);
    setError('');
    const request = runID ? getRunGraph(runID) : graphID ? getGraph(graphID) : Promise.reject(new Error('Граф не указан.'));
    request.then((next) => {
      if (active) {
        setView(next);
        setError('');
      }
    }).catch((reason: unknown) => {
      if (active) setError(reason instanceof Error ? reason.message : 'Не удалось загрузить граф.');
    }).finally(() => {
      if (active) setLoading(false);
    });
    return () => { active = false; };
  }, [graphID, runID]);

  const layout = useMemo(() => (view ? buildLayout(view) : null), [view]);

  if (loading) return <main className={styles.state}>Загружаем граф…</main>;
  if (error || !view || !layout) {
    return <main className={styles.state} role="alert">{error || 'Граф не найден.'}</main>;
  }

  const runMode = view.mode === 'run';
  const backHref = runMode && view.run_id
    ? `/tasks/${encodeURIComponent(view.run_id)}`
    : view.workflow_id ? `/workflows/${encodeURIComponent(view.workflow_id)}` : '/workflows';
  const backLabel = runMode ? 'К задаче' : 'К workflow';
  const statusByKey = new Map((view.stage_statuses?.stages ?? []).map((entry) => [entry.stage_id, entry]));
  const currentKey = view.current_stage || view.stage_statuses?.current_stage || '';
  const currentStatus = currentKey ? statusByKey.get(currentKey) : undefined;

  return (
    <main className={styles.container}>
      <header className={styles.header}>
        <Link to={backHref} className={styles.backLink}>← {backLabel}</Link>
        <h1>{view.title || (runMode ? 'Граф прогона' : 'Граф workflow')}</h1>
        <p className={styles.lede}>
          {runMode
            ? 'Прогон задачи: текущий этап подсвечен, исходящие переходы показаны потоком.'
            : 'Класс графа: набор этапов и переходов workflow без привязки к задаче.'}
        </p>
        <dl className={styles.meta}>
          <div><dt>Режим</dt><dd>{runMode ? 'Прогон' : 'Класс'}</dd></div>
          {view.graph_id && <div><dt>Граф</dt><dd>{view.graph_id}</dd></div>}
          {view.workflow_id && <div><dt>Workflow</dt><dd>{view.workflow_id}</dd></div>}
          {runMode && view.run_status && <div><dt>Задача</dt><dd>{view.run_status}</dd></div>}
          {runMode && currentStatus && <div><dt>Сейчас</dt><dd>{currentStatus.title || currentStatus.stage_id}</dd></div>}
        </dl>
      </header>

      <section className={styles.panel} aria-labelledby="graph-diagram-heading">
        <h2 id="graph-diagram-heading">Схема переходов</h2>
        <div className={styles.canvas}>
          <svg className={styles.svg} viewBox={`0 0 ${layout.width} ${layout.height}`}
            preserveAspectRatio="xMidYMin meet" role="group" aria-label="Схема графа">
            <defs>
              <marker id={`${markerPrefix}-arrow`} viewBox="0 0 10 10" refX="8" refY="5"
                markerWidth="7" markerHeight="7" orient="auto-start-reverse">
                <path d="M 0 0 L 10 5 L 0 10 z" className={styles.arrow} />
              </marker>
              <marker id={`${markerPrefix}-arrow-active`} viewBox="0 0 10 10" refX="8" refY="5"
                markerWidth="7" markerHeight="7" orient="auto-start-reverse">
                <path d="M 0 0 L 10 5 L 0 10 z" className={styles.arrowActive} />
              </marker>
            </defs>
            <g className={styles.scene}>
              {layout.edges.map((edge) => (
                <g key={edge.id} className={`${styles.edge} ${edge.backward ? styles.edgeBack : ''} ${edge.active ? styles.edgeActive : ''}`}
                  data-active={edge.active || undefined}>
                  <path d={edge.path} className={styles.edgeLine}
                    markerEnd={`url(#${markerPrefix}-${edge.active ? 'arrow-active' : 'arrow'})`} />
                  <path d={edge.path} className={styles.edgeFlow} />
                  <rect x={edge.labelX - 4} y={edge.labelY - 13}
                    width={Math.max(40, edge.label.length * 6 + 10)} height={19} rx={5}
                    className={styles.edgeLabelBg} />
                  <text x={edge.labelX} y={edge.labelY} className={styles.edgeLabel}>{edge.label}</text>
                </g>
              ))}
              {layout.nodes.map((node) => {
                const href = node.terminal ? undefined : runMode
                  ? (view.run_id ? `/tasks/${encodeURIComponent(view.run_id)}` : undefined)
                  : (view.workflow_id ? `/workflows/${encodeURIComponent(view.workflow_id)}` : undefined);
                const ariaLabel = runMode
                  ? `Открыть задачу: ${node.label}`
                  : `Открыть workflow: ${view.workflow_id ?? node.label}`;
                const body = (
                  <g className={`${styles.node} ${statusClass(node.status)} ${node.current ? styles.nodeCurrent : ''}`}
                    data-status={runMode ? node.status?.status ?? 'not_reached' : undefined}
                    data-current={node.current || undefined}>
                    <rect x={node.pos.x} y={node.pos.y} width={NODE_W} height={NODE_H} rx={14}
                      className={styles.nodeRect} />
                    <text x={node.pos.x + 14} y={node.pos.y + 27} className={styles.nodeLabel}>{node.label}</text>
                    {node.sub && <text x={node.pos.x + 14} y={node.pos.y + 47} className={styles.nodeSub}>{node.sub}</text>}
                  </g>
                );
                return href
                  ? <Link key={node.key} to={href} className={styles.nodeLink} aria-label={ariaLabel}
                      aria-current={node.current ? 'step' : undefined}>{body}</Link>
                  : <g key={node.key}>{body}</g>;
              })}
            </g>
          </svg>
        </div>
        <ul className={styles.legend} aria-label="Обозначения">
          <li><span className={`${styles.swatch} ${styles.swatchNode}`} /> Этап</li>
          <li><span className={`${styles.swatch} ${styles.swatchCurrent}`} /> Текущий этап</li>
          <li><span className={`${styles.swatch} ${styles.swatchVisited}`} /> Пройден</li>
          <li><span className={`${styles.swatch} ${styles.swatchFailed}`} /> Ошибка</li>
          <li><span className={`${styles.swatch} ${styles.swatchEdge}`} /> Переход (outcome)</li>
          <li><span className={`${styles.swatch} ${styles.swatchReturn}`} /> Возврат (return_to_*)</li>
          <li><span className={`${styles.swatch} ${styles.swatchTerminal}`} /> Завершение</li>
        </ul>
      </section>
    </main>
  );
}
