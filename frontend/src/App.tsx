import { BrowserRouter, Routes, Route, Navigate } from "react-router-dom"
import { Layout } from "./components/layout/Layout"
import Dashboard from "./pages/Dashboard"
import LoadBalancers from "./pages/LoadBalancers"
import LoadBalancerDetail from "./pages/LoadBalancerDetail"
import Login from "./pages/Login"
import { Settings } from "./pages/Settings"
import Logs from "./pages/Logs"

function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const token = localStorage.getItem("token")
  if (!token) {
    return <Navigate to="/login" replace />
  }
  return <Layout>{children}</Layout>
}

function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<Login />} />
        
        <Route path="/" element={<ProtectedRoute><Dashboard /></ProtectedRoute>} />
        <Route path="/lbs" element={<ProtectedRoute><LoadBalancers /></ProtectedRoute>} />
        <Route path="/lbs/:id" element={<ProtectedRoute><LoadBalancerDetail /></ProtectedRoute>} />
        <Route path="/settings" element={<ProtectedRoute><Settings /></ProtectedRoute>} />
        <Route path="/logs" element={<ProtectedRoute><Logs /></ProtectedRoute>} />
      </Routes>
    </BrowserRouter>
  )
}

export default App
