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

export interface CreateTaskOptions {
  title?: string
  gitUrl?: string
  env?: Record<string, string>
}

export async function createTask(username: string, options?: CreateTaskOptions): Promise<string> {
  const res = await fetch(`${BASE}/api/tasks`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify({
      username,
      title: options?.title,
      git_url: options?.gitUrl,
      env: options?.env,
    }),
  })
  if (!res.ok) {
    const data = await res.json().catch(() => ({})) as { error?: string }
    throw new Error(data.error ?? 'Failed to create task')
  }
  const { id } = await res.json() as { id: string }
  return id
}

export async function sendMessage(taskId: string, prompt: string, files?: File[]): Promise<Response> {
  if (files && files.length > 0) {
    const form = new FormData()
    form.append('prompt', prompt)
    for (const f of files) form.append('files', f)
    return fetch(`${BASE}/api/tasks/${taskId}/messages`, {
      method: 'POST',
      headers: authHeaders(),
      body: form,
    })
  }
  return fetch(`${BASE}/api/tasks/${taskId}/messages`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify({ prompt }),
  })
}

export async function steerMessage(
  taskId: string,
  prompt: string,
  priority?: 'now' | 'next' | 'later',
): Promise<void> {
  const res = await fetch(`${BASE}/api/tasks/${taskId}/steer`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify({ prompt, priority }),
  })
  if (!res.ok) throw new Error(`steer failed: ${res.status}`)
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
  git_url?: string
  error_msg?: string
  schedule_id?: string
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
  git_url?: string
  error_msg?: string
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

export interface UserSettings {
  has_ssh_key: boolean
  has_anthropic_key: boolean
}

export async function getUserSettings(): Promise<UserSettings> {
  const res = await fetch(`${BASE}/api/user/settings`, {
    headers: authHeaders(),
  })
  if (!res.ok) throw new Error('Failed to get user settings')
  return res.json() as Promise<UserSettings>
}

export async function updateUserSettings(body: { ssh_private_key?: string; anthropic_api_key?: string }): Promise<void> {
  const res = await fetch(`${BASE}/api/user/settings`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const data = await res.json().catch(() => ({})) as { error?: string }
    throw new Error(data.error ?? 'Failed to update settings')
  }
}

// ---- schedule API ----

export interface Schedule {
  id: string
  title: string
  prompt: string
  cron_expr: string
  run_at?: string
  extra_env?: Record<string, string>
  git_url?: string
  timeout_secs: number
  concurrency: number
  enabled: boolean
  last_run_at?: string
  next_run_at?: string
  created_at: string
}

export interface CreateSchedulePayload {
  title?: string
  prompt: string
  cron_expr: string
  run_at?: string
  extra_env?: Record<string, string>
  git_url?: string
  timeout_secs?: number
  concurrency?: number
}

export interface UpdateSchedulePayload {
  title?: string
  prompt?: string
  cron_expr?: string
  run_at?: string
  extra_env?: Record<string, string>
  git_url?: string
  timeout_secs?: number
  concurrency?: number
}

export interface ScheduleRun {
  id: string
  title: string
  state: string
  error_msg?: string
  created_at: string
  updated_at: string
}

export async function listSchedules(): Promise<Schedule[]> {
  const res = await fetch(`${BASE}/api/schedules`, { headers: authHeaders() })
  if (!res.ok) throw new Error('Failed to list schedules')
  return res.json() as Promise<Schedule[]>
}

export async function createSchedule(payload: CreateSchedulePayload): Promise<Schedule> {
  const res = await fetch(`${BASE}/api/schedules`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify(payload),
  })
  if (!res.ok) {
    const data = await res.json().catch(() => ({})) as { error?: string }
    throw new Error(data.error ?? 'Failed to create schedule')
  }
  return res.json() as Promise<Schedule>
}

export async function getSchedule(id: string): Promise<Schedule> {
  const res = await fetch(`${BASE}/api/schedules/${id}`, { headers: authHeaders() })
  if (!res.ok) throw new Error('Failed to get schedule')
  return res.json() as Promise<Schedule>
}

export async function updateSchedule(id: string, payload: UpdateSchedulePayload): Promise<Schedule> {
  const res = await fetch(`${BASE}/api/schedules/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify(payload),
  })
  if (!res.ok) {
    const data = await res.json().catch(() => ({})) as { error?: string }
    throw new Error(data.error ?? 'Failed to update schedule')
  }
  return res.json() as Promise<Schedule>
}

export async function deleteSchedule(id: string): Promise<void> {
  await fetch(`${BASE}/api/schedules/${id}`, { method: 'DELETE', headers: authHeaders() })
}

export async function enableSchedule(id: string): Promise<void> {
  await fetch(`${BASE}/api/schedules/${id}/enable`, { method: 'POST', headers: authHeaders() })
}

export async function disableSchedule(id: string): Promise<void> {
  await fetch(`${BASE}/api/schedules/${id}/disable`, { method: 'POST', headers: authHeaders() })
}

export async function runScheduleNow(id: string): Promise<{ task_id: string }> {
  const res = await fetch(`${BASE}/api/schedules/${id}/run`, { method: 'POST', headers: authHeaders() })
  if (!res.ok) throw new Error('Failed to trigger run')
  return res.json() as Promise<{ task_id: string }>
}

export async function listScheduleRuns(id: string): Promise<ScheduleRun[]> {
  const res = await fetch(`${BASE}/api/schedules/${id}/runs`, { headers: authHeaders() })
  if (!res.ok) throw new Error('Failed to list runs')
  return res.json() as Promise<ScheduleRun[]>
}
