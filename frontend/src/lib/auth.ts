const TOKEN_KEY = 'lucas_token'

export interface JWTPayload {
  user_id: number
  user_name: string
  exp: number
}

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}

export function setToken(token: string): void {
  localStorage.setItem(TOKEN_KEY, token)
}

export function clearToken(): void {
  localStorage.removeItem(TOKEN_KEY)
}

export function decodeToken(token: string): JWTPayload | null {
  try {
    return JSON.parse(atob(token.split('.')[1])) as JWTPayload
  } catch {
    return null
  }
}

export function getAuthUsername(): string | null {
  const token = getToken()
  if (!token) return null
  const payload = decodeToken(token)
  if (!payload || payload.exp * 1000 <= Date.now()) return null
  return payload.user_name
}
