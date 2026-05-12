import { useEffect } from 'react'
import { useNavigate, useLocation } from 'react-router-dom'
import { setToken } from '@/lib/auth'

export function SSOCallbackPage() {
  const { hash } = useLocation()
  const navigate = useNavigate()

  useEffect(() => {
    const token = new URLSearchParams(hash.slice(1)).get('access_token')
    if (token) {
      setToken(token)
      navigate('/', { replace: true })
    } else {
      navigate('/login', { replace: true })
    }
  }, [hash, navigate])

  return (
    <div className="flex items-center justify-center h-[100dvh]">
      <p className="text-neutral-400 text-sm">Signing in…</p>
    </div>
  )
}
