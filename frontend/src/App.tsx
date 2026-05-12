import { BrowserRouter, Routes, Route } from 'react-router-dom'
import { ChatPage } from '@/pages/ChatPage'
import { LoginPage } from '@/pages/LoginPage'
import { SSOCallbackPage } from '@/pages/SSOCallbackPage'
import { ProtectedRoute } from '@/components/ProtectedRoute'
import './index.css'

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="/login/sso" element={<SSOCallbackPage />} />
        <Route path="/login/oidc" element={<SSOCallbackPage />} />
        <Route
          path="/"
          element={
            <ProtectedRoute>
              <ChatPage />
            </ProtectedRoute>
          }
        />
      </Routes>
    </BrowserRouter>
  )
}
