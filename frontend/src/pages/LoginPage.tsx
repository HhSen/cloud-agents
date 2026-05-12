import { useEffect, useState } from 'react'
import { getRuntimeConfig, type RuntimeConfig } from '@/api/client'

const BASE = import.meta.env.VITE_API_BASE ?? ''

export function LoginPage() {
  const [config, setConfig] = useState<RuntimeConfig | null>(null)
  const [username, setUsername] = useState('')

  useEffect(() => {
    getRuntimeConfig().then(setConfig).catch(() => setConfig({ loginMode: 'none', devLogin: false }))
  }, [])

  const handleSSOLogin = () => {
    window.location.href = `${BASE}/api/auth/sso/login`
  }

  const handleDevLogin = () => {
    if (username.trim()) {
      window.location.href = `${BASE}/api/auth/dev/login?username=${encodeURIComponent(username.trim())}`
    }
  }

  return (
    <div className="flex flex-col items-center justify-center h-[100dvh]">
      <div className="flex flex-col gap-3 w-full max-w-sm px-4">
        <h1 className="text-lg font-semibold text-center">Welcome to Lucas</h1>

        {config?.loginMode.includes('sso') && (
          <>
            <p className="text-sm text-neutral-500 text-center">Sign in to continue</p>
            <button
              className="bg-neutral-900 text-white rounded-md px-4 py-2 text-sm font-medium hover:bg-neutral-700 transition-colors"
              onClick={handleSSOLogin}
            >
              Login with SSO
            </button>
          </>
        )}

        {config?.loginMode.includes('oidc') && (
          <button
            className="bg-neutral-900 text-white rounded-md px-4 py-2 text-sm font-medium hover:bg-neutral-700 transition-colors"
            onClick={() => { window.location.href = `${BASE}/api/auth/oidc/login` }}
          >
            Login with OIDC
          </button>
        )}

        {config?.devLogin && (
          <>
            <p className="text-sm text-neutral-500 text-center">Dev mode — enter any username</p>
            <input
              className="border border-neutral-200 rounded-md px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-neutral-300"
              placeholder="Username"
              value={username}
              onChange={e => setUsername(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter') handleDevLogin() }}
              autoFocus
            />
            <button
              className="bg-neutral-900 text-white rounded-md px-4 py-2 text-sm font-medium disabled:opacity-50"
              disabled={!username.trim()}
              onClick={handleDevLogin}
            >
              Continue
            </button>
          </>
        )}

        {!config && (
          <p className="text-sm text-neutral-400 text-center">Loading…</p>
        )}
      </div>
    </div>
  )
}
