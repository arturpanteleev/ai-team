export interface FlowDurationStats {
  count: number;
  median_ms?: number;
  p80_ms?: number;
}

export interface FlowStageStateStats {
  execution: string;
  decision: string;
  outcome: string;
  attempts: number;
  duration_ms: number;
}

export interface FlowStageStats {
  stage: string;
  attempts: number;
  duration_ms: number;
  human_attempts: number;
  agent_attempts: number;
  agent_share: number | null;
  by_state: FlowStageStateStats[];
  tokens_input: number | null;
  tokens_output: number | null;
  usage_known_attempts: number;
  usage_unknown_attempts: number;
  usage_attestation: string;
  usage_source?: string;
  subscription_share_estimate: number | null;
  subscription_currency?: string;
  estimate_source?: string;
  estimate_basis?: string;
}

export interface FlowMetrics {
  from: string;
  to: string;
  lead_time: FlowDurationStats;
  cycle_time: FlowDurationStats;
  by_stage: FlowStageStats[];
  overrides_by_stage: Record<string, number>;
  skips_by_stage: Record<string, number>;
  returns_by_stage: Record<string, number>;
  usage: {
    known_runs: number;
    unknown_runs: number;
    tokens_input: number | null;
    tokens_output: number | null;
    attestation: string;
    source: string;
    subscription_share_estimate: number | null;
    subscription_currency?: string;
    estimate_source?: string;
    estimate_basis?: string;
  };
  agent_results: number;
  agent_accepted_without_edit: number;
  agent_accepted_without_edit_rate?: number | null;
}
