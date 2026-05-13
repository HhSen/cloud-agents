import { getToken } from '@/lib/auth'
import type { SessionEntry } from '@/types/session'

const BASE = import.meta.env.VITE_API_BASE ?? ''

function authHeaders(): Record<string, string> {
  const token = getToken()
  return token ? { Authorization: `Bearer ${token}` } : {}
}

export interface RuntimeConfig {
  loginMode: string
  passwordLogin: boolean
  allowRegister: boolean
}

export async function getRuntimeConfig(): Promise<RuntimeConfig> {
  const res = await fetch(`${BASE}/api/runtime-config`)
  if (!res.ok) throw new Error('Failed to fetch runtime config')
  return res.json() as Promise<RuntimeConfig>
}

export async function loginWithPassword(username: string, password: string): Promise<string> {
  const res = await fetch(`${BASE}/api/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  if (!res.ok) throw new Error('Invalid credentials')
  const { access_token } = await res.json() as { access_token: string }
  return access_token
}

export async function register(username: string, password: string, email?: string): Promise<string> {
  const res = await fetch(`${BASE}/api/auth/register`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password, email }),
  })
  if (res.status === 409) throw new Error('Username already taken')
  if (!res.ok) throw new Error('Registration failed')
  const { access_token } = await res.json() as { access_token: string }
  return access_token
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
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify({ decision }),
  })
}

export async function respondToQuestion(taskId: string, answers: Record<string, string | string[]>): Promise<void> {
  await fetch(`${BASE}/api/tasks/${taskId}/questions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify({ answers }),
  })
}

export interface TaskSummary {
  id: string
  title: string
  state: string
  created_at: string
  updated_at: string
}

export async function listTasks(): Promise<TaskSummary[]> {
  const res = await fetch(`${BASE}/api/tasks`, {
    headers: authHeaders(),
  })
  if (!res.ok) throw new Error('Failed to list tasks')
  return res.json() as Promise<TaskSummary[]>
}

export async function getHistory(taskId: string): Promise<SessionEntry[]> {
  const res = await fetch(`${BASE}/api/tasks/${taskId}/history`, {
    headers: authHeaders(),
  })
  if (!res.ok) throw new Error('Failed to get history')
  return res.json() as Promise<SessionEntry[]>
}

export interface Resource {
  id: number
  kind: 'skill' | 'mcp'
  name: string
  ofs_path: string
  meta: Record<string, unknown>
  is_active: boolean
  created_at: string
  updated_at: string
}

export interface CreateResourcePayload {
  kind: 'skill' | 'mcp'
  name: string
  content: string
  meta?: Record<string, unknown>
}

export interface UpdateResourcePayload {
  content?: string
  meta?: Record<string, unknown>
  is_active?: boolean
}

export async function listResources(): Promise<Resource[]> {
  const res = await fetch(`${BASE}/api/resources`, {
    headers: authHeaders(),
  })
  if (!res.ok) throw new Error('Failed to list resources')
  return res.json() as Promise<Resource[]>
}

export async function createResource(payload: CreateResourcePayload): Promise<Resource> {
  const res = await fetch(`${BASE}/api/resources`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify(payload),
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({})) as { error?: string }
    throw new Error(body.error ?? 'Failed to create resource')
  }
  return res.json() as Promise<Resource>
}

export async function updateResource(id: number, payload: UpdateResourcePayload): Promise<Resource> {
  const res = await fetch(`${BASE}/api/resources/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify(payload),
  })
  if (!res.ok) throw new Error('Failed to update resource')
  return res.json() as Promise<Resource>
}

export async function createSkillFromZip(name: string, file: File): Promise<Resource> {
  const form = new FormData()
  form.append('name', name)
  form.append('file', file)
  const res = await fetch(`${BASE}/api/resources/zip`, {
    method: 'POST',
    headers: authHeaders(),
    body: form,
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({})) as { error?: string }
    throw new Error(body.error ?? 'Failed to create skill')
  }
  return res.json() as Promise<Resource>
}

export async function getSkillContent(id: number): Promise<string> {
  const res = await fetch(`${BASE}/api/resources/${id}/content`, {
    headers: authHeaders(),
  })
  if (!res.ok) throw new Error('Failed to fetch skill content')
  return res.text()
}

export async function deleteResource(id: number): Promise<void> {
  const res = await fetch(`${BASE}/api/resources/${id}`, {
    method: 'DELETE',
    headers: authHeaders(),
  })
  if (!res.ok && res.status !== 404) throw new Error('Failed to delete resource')
}

export interface FileInfo {
  path: string
  name: string
  isDir: boolean
  size: number
  mode: string
  modTime: string
}

export interface Task {
  id: string
  username: string
  state: string
  sandbox_id: string
  session_id: string
  title: string
  cwd: string
}

export async function getTask(taskId: string): Promise<Task> {
  const res = await fetch(`${BASE}/api/tasks/${taskId}`, {
    headers: authHeaders(),
  })
  if (!res.ok) throw new Error('Failed to get task')
  return res.json() as Promise<Task>
}

export async function listDir(taskId: string, dir: string): Promise<FileInfo[]> {
  const params = new URLSearchParams({ path: dir })
  const res = await fetch(`${BASE}/api/tasks/${taskId}/workspace/files?${params}`, {
    headers: authHeaders(),
  })
  if (!res.ok) throw new Error('Failed to list directory')
  return res.json() as Promise<FileInfo[]>
}

export async function readFile(taskId: string, filePath: string): Promise<string> {
  const params = new URLSearchParams({ path: filePath })
  const res = await fetch(`${BASE}/api/tasks/${taskId}/workspace/file?${params}`, {
    headers: authHeaders(),
  })
  if (!res.ok) throw new Error('Failed to read file')
  return res.text()
}
