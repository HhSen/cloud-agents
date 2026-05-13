import { ProtectedRoute } from '@/components/ProtectedRoute'
import { ChatPage } from '@/pages/ChatPage'
import { LoginPage } from '@/pages/LoginPage'
import { ResourcesPage } from '@/pages/ResourcesPage'
import { SSOCallbackPage } from '@/pages/SSOCallbackPage'
import { Agentation } from 'agentation'
import { BrowserRouter, Route, Routes } from 'react-router-dom'
import './index.css'

export default function App() {
  return (
    <>
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
          <Route
            path="/resources"
            element={
              <ProtectedRoute>
                <ResourcesPage />
              </ProtectedRoute>
            }
          />
        </Routes>
      </BrowserRouter>
      <Agentation />
    </>
  )
}
