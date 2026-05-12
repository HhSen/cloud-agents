import { Navigate } from 'react-router-dom'
import { getAuthUsername } from '@/lib/auth'

export function ProtectedRoute({ children }: { children: React.ReactNode }) {
  if (!getAuthUsername()) return <Navigate to="/login" replace />
  return <>{children}</>
}
