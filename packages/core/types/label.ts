/**
 * Issue labels — workspace-scoped, applied as many-to-many to issues.
 *
 * Labels are lightweight metadata (name + color) distinct from projects:
 * projects group related work, labels are cross-cutting tags (bug, feature,
 * performance, …). Colors are normalized to lowercase `#RRGGBB`.
 */
export type LabelResourceType = "issue" | "agent" | "skill";

export interface Label {
  id: string;
  workspace_id: string;
  resource_type?: LabelResourceType;
  name: string;
  description?: string;
  /** Normalized lowercase hex color, e.g. `#3b82f6`. */
  color: string;
  usage_count?: number;
  /** Issue-task usage rollup. Present on issue label catalog responses. */
  usage_summary?: LabelUsageSummary;
  created_at: string;
  updated_at: string;
}

export type LabelUsagePeriod = "7d" | "30d" | "90d" | "all";
export type LabelUsageSort = "cost" | "tokens" | "recent";
export type LabelUsageDirection = "asc" | "desc";

/**
 * Cost is the priced portion only. `unpriced_task_count` makes partial totals
 * explicit so clients never present a known subtotal as the complete bill.
 */
export interface LabelUsageSummary {
  total_tokens: number;
  total_cost_usd_ticks: number;
  uncosted_tokens: number;
  task_count: number;
  priced_task_count: number;
  unpriced_task_count: number;
}

export interface LabelUsageDaily {
  date: string;
  total_tokens: number;
  total_cost_usd_ticks: number;
  uncosted_tokens: number;
  task_count: number;
  priced_task_count: number;
  unpriced_task_count: number;
}

export interface LabelUsageBreakdown {
  provider: string;
  model: string;
  total_tokens: number;
  total_cost_usd_ticks: number;
  uncosted_tokens: number;
  task_count: number;
  unpriced_task_count: number;
}

export interface LabelUsageTaskBreakdown {
  provider: string;
  model: string;
  total_tokens: number;
  total_cost_usd_ticks: number;
  uncosted_tokens: number;
  is_priced: boolean;
}

export interface LabelUsageTask {
  task_id: string;
  issue_id: string;
  issue_identifier: string;
  issue_title: string;
  status: string;
  provider: string;
  model: string;
  has_usage: boolean;
  total_tokens: number;
  total_cost_usd_ticks: number;
  uncosted_tokens: number;
  is_priced: boolean;
  usage_breakdown: LabelUsageTaskBreakdown[];
  created_at: string;
  completed_at?: string | null;
  activity_at: string;
}

export interface LabelUsagePagination {
  page: number;
  page_size: number;
  total: number;
  total_pages: number;
}

export interface LabelUsageResponse {
  label: Label;
  summary: LabelUsageSummary;
  daily: LabelUsageDaily[];
  breakdown: LabelUsageBreakdown[];
  tasks: LabelUsageTask[];
  pagination: LabelUsagePagination;
}

export interface LabelUsageParams {
  period: LabelUsagePeriod;
  sort: LabelUsageSort;
  direction: LabelUsageDirection;
  tz: string;
  page: number;
  page_size: number;
}

export interface CreateLabelRequest {
  resource_type?: LabelResourceType;
  name: string;
  description?: string;
  color: string;
}

export interface UpdateLabelRequest {
  name?: string;
  description?: string;
  color?: string;
}

export interface ListLabelsResponse {
  labels: Label[];
  total: number;
}

export interface IssueLabelsResponse {
  labels: Label[];
}

export type ResourceLabelsResponse = IssueLabelsResponse;
