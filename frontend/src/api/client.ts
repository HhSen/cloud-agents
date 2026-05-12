import { getToken } from '@/lib/auth'

const BASE = import.meta.env.VITE_API_BASE ?? ''

function authHeaders(): Record<string, string> {
  const token = getToken()
  return token ? { Authorization: `Bearer ${token}` } : {}
}

export interface RuntimeConfig {
  loginMode: string
  devLogin: boolean
}

export async function getRuntimeConfig(): Promise<RuntimeConfig> {
  const res = await fetch(`${BASE}/api/runtime-config`)
  if (!res.ok) throw new Error('Failed to fetch runtime config')
  return res.json() as Promise<RuntimeConfig>
}

export async function createTask(username: string): Promise<string> {
  const res = await fetch(`${BASE}/api/tasks`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify({ username }),
  })
  if (!res.ok) throw new Error('Failed to create task')
  const { id } = await res.json() as { id: string }
  return id
}

export async function sendMessage(taskId: string, prompt: string): Promise<Response> {
  return fetch(`${BASE}/api/tasks/${taskId}/messages`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify({ prompt }),
  })
}

export async function deleteTask(taskId: string): Promise<void> {
  await fetch(`${BASE}/api/tasks/${taskId}`, {
    method: 'DELETE',
    headers: authHeaders(),
  })
}

export async function respondToPermission(taskId: string, decision: 'allow' | 'deny'): Promise<void> {
  await fetch(`${BASE}/api/tasks/${taskId}/permissions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ decision }),
  })
}

export async function respondToQuestion(taskId: string, answers: Record<string, string | string[]>): Promise<void> {
  await fetch(`${BASE}/api/tasks/${taskId}/questions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ answers }),
  })
}
