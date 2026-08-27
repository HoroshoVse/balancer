import { BrowserRouter, Routes, Route, Navigate } from "react-router-dom"
import { Layout } from "./components/layout/Layout"
import Dashboard from "./pages/Dashboard"
import LoadBalancers from "./pages/LoadBalancers"
import Login from "./pages/Login"

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
      </Routes>
    </BrowserRouter>
  )
}

export default App
