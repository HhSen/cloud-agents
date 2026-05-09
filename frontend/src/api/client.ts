const BASE = import.meta.env.VITE_API_BASE ?? ''

export async function createConversation(): Promise<string> {
  const res = await fetch(`${BASE}/api/conversations`, { method: 'POST' })
  if (!res.ok) throw new Error('Failed to create conversation')
  const { id } = await res.json() as { id: string }
  return id
}

export async function sendMessage(convId: string, prompt: string): Promise<Response> {
  return fetch(`${BASE}/api/conversations/${convId}/messages`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ prompt }),
  })
}

export async function deleteConversation(convId: string): Promise<void> {
  await fetch(`${BASE}/api/conversations/${convId}`, { method: 'DELETE' })
}
