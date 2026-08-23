export interface AgentStep {
  tool: string
  args: Record<string, unknown>
  result: string
}

export interface AgentResponse {
  answer: string
  model: string
  steps: AgentStep[]
}

interface AgentWireResponse {
  answer?: unknown
  model?: unknown
  steps?: Array<Partial<AgentStep>> | null
}

export function normalizeAgentResponse(value: AgentWireResponse): AgentResponse {
  return {
    answer: typeof value.answer === 'string' ? value.answer : '',
    model: typeof value.model === 'string' ? value.model : '',
    steps: Array.isArray(value.steps) ? value.steps.map((step) => ({
      tool: typeof step.tool === 'string' ? step.tool : '',
      args: step.args && typeof step.args === 'object' ? step.args : {},
      result: typeof step.result === 'string' ? step.result : '',
    })) : [],
  }
}
