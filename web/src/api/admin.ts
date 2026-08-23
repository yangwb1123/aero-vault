export type AdminJobStatus = 'pending' | 'running' | 'succeeded' | 'failed'

export type AdminJob = {
  id: number
  tenant: string
  type: string
  payload?: string
  status: AdminJobStatus | string
  priority?: number
  attempts: number
  max_attempts: number
  run_after?: string
  last_error?: string
  worker?: string
  result?: string
  created_at?: string
  started_at?: string
  finished_at?: string
}

export interface AdminJobsResponse {
  stats: Record<string, number>
  jobs: AdminJob[]
}

export type AdminTenant = {
  tenant_id: string
  display_name: string
  status: string
  created_at: string
}

export type AdminWebhookFailure = {
  id: number
  eventId: number
  url: string
  attempts: number
  lastError: string
  lastStatus: number
  nextRetryAt: string
  succeeded: boolean
  deadLettered: boolean
  createdAt: string
}

type WireRecord = Record<string, unknown>

export function normalizeWebhookFailure(value: unknown): AdminWebhookFailure {
  const row = value && typeof value === 'object' ? value as WireRecord : {}
  return {
    id: numberField(row, 'id', 'ID'),
    eventId: numberField(row, 'event_id', 'EventID'),
    url: stringField(row, 'url', 'URL'),
    attempts: numberField(row, 'attempts', 'Attempts'),
    lastError: stringField(row, 'last_error', 'LastError'),
    lastStatus: numberField(row, 'last_status', 'LastStatus'),
    nextRetryAt: stringField(row, 'next_retry_at', 'NextRetryAt'),
    succeeded: booleanField(row, 'succeeded', 'Succeeded'),
    deadLettered: booleanField(row, 'dead_lettered', 'DeadLettered'),
    createdAt: stringField(row, 'created_at', 'CreatedAt'),
  }
}

function field(row: WireRecord, jsonName: string, goName: string): unknown {
  return row[jsonName] ?? row[goName]
}

function stringField(row: WireRecord, jsonName: string, goName: string): string {
  const value = field(row, jsonName, goName)
  return typeof value === 'string' ? value : ''
}

function numberField(row: WireRecord, jsonName: string, goName: string): number {
  const value = Number(field(row, jsonName, goName))
  return Number.isFinite(value) ? value : 0
}

function booleanField(row: WireRecord, jsonName: string, goName: string): boolean {
  return field(row, jsonName, goName) === true
}
