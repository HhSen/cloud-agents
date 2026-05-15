import { ProtectedRoute } from '@/components/ProtectedRoute'
import { ChatPage } from '@/pages/ChatPage'
import { LoginPage } from '@/pages/LoginPage'
import { ResourcesPage } from '@/pages/ResourcesPage'
import { ScheduleDetailPage } from '@/pages/ScheduleDetailPage'
import { ScheduleFormPage } from '@/pages/ScheduleFormPage'
import { SchedulesPage } from '@/pages/SchedulesPage'
import { SettingsPage } from '@/pages/SettingsPage'
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
          <Route
            path="/settings"
            element={
              <ProtectedRoute>
                <SettingsPage />
              </ProtectedRoute>
            }
          />
          <Route
            path="/schedules"
            element={
              <ProtectedRoute>
                <SchedulesPage />
              </ProtectedRoute>
            }
          />
          <Route
            path="/schedules/new"
            element={
              <ProtectedRoute>
                <ScheduleFormPage mode="create" />
              </ProtectedRoute>
            }
          />
          <Route
            path="/schedules/:id"
            element={
              <ProtectedRoute>
                <ScheduleDetailPage />
              </ProtectedRoute>
            }
          />
          <Route
            path="/schedules/:id/edit"
            element={
              <ProtectedRoute>
                <ScheduleFormPage mode="edit" />
              </ProtectedRoute>
            }
          />
        </Routes>
      </BrowserRouter>
      <Agentation />
    </>
  )
}
